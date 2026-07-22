// Package main is the entrypoint for the document store server.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/muradImre/database-design/auth"
	"github.com/muradImre/database-design/btreeidx"
	"github.com/muradImre/database-design/db"
	"github.com/muradImre/database-design/dbServer"
	"github.com/muradImre/database-design/patch"
	"github.com/muradImre/database-design/schema/parser"
	"github.com/muradImre/database-design/schema/validator"
	"github.com/muradImre/database-design/shardedidx"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

type SchemaValidatorFactory struct {
	schema *jsonschema.Schema
}

func (f *SchemaValidatorFactory) NewValidator() dbServer.Validator {
	return validator.New(f.schema)
}

type AuthManagerFactory struct {
	am *auth.AuthManager
}

func (f *AuthManagerFactory) NewAuthManager() dbServer.AuthService {
	return f.am
}

func main() {
	var port int
	var schemaPath, tokensPath, dataPath, indexKind string
	var verbose bool

	flag.IntVar(&port, "port", 8080, "TCP port to listen on")
	flag.StringVar(&schemaPath, "schema", "schema.json", "JSON Schema file used to validate documents")
	flag.StringVar(&tokensPath, "tokens", "tokens.json", "JSON file mapping usernames to preloaded access tokens")
	flag.StringVar(&dataPath, "data", "data/snapshot.json", "snapshot file for disk persistence (empty disables it)")
	flag.StringVar(&indexKind, "index", "cow", "index implementation: cow (copy-on-write, lock-free reads) or sharded (concurrent writers)")
	flag.BoolVar(&verbose, "verbose", false, "enable debug logging")
	flag.Parse()

	if indexKind != "cow" && indexKind != "sharded" {
		fmt.Printf("invalid -index %q: want \"cow\" or \"sharded\"\n", indexKind)
		os.Exit(2)
	}

	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	sch, err := parser.SchemaParser(schemaPath)
	if err != nil {
		slog.Error("failed to load schema", "error", err)
		return
	}

	// tokens.json is username -> token; AuthManager stores token -> username.
	usernameToToken := make(map[string]string)
	tokenFile, err := os.ReadFile(tokensPath)
	if err == nil {
		if err := json.Unmarshal(tokenFile, &usernameToToken); err != nil {
			slog.Error("failed to parse tokens file", "error", err)
			return
		}
	} else {
		fmt.Printf("Tokens file not found (%s); only login-issued tokens will work.\n", tokensPath)
	}

	existingTokens := make(map[string]string)
	for username, token := range usernameToToken {
		existingTokens[token] = username
	}

	authManager := auth.NewAuthManager(existingTokens)
	authManager.SetExistingTokens()
	authFactory := &AuthManagerFactory{am: authManager}

	docIndexFactory := func() db.DBIndex[string, db.Document] {
		if indexKind == "sharded" {
			return shardedidx.New[string, db.Document]()
		}
		return btreeidx.New[string, db.Document]()
	}
	dbFactory := func(name string) dbServer.DB {
		return db.New(name, docIndexFactory)
	}
	databasesFactory := func() dbServer.DBIndex[string, dbServer.DB] {
		if indexKind == "sharded" {
			return shardedidx.New[string, dbServer.DB]()
		}
		return btreeidx.New[string, dbServer.DB]()
	}

	validatorFactory := &SchemaValidatorFactory{schema: sch}
	patchApplier := patch.New()

	server := dbServer.NewManaged(port, authFactory, dbFactory, databasesFactory, validatorFactory, patchApplier)
	server.SetSnapshotPath(dataPath)

	if err := server.LoadSnapshot(); err != nil {
		slog.Error("failed to load snapshot", "error", err)
	}

	ctrlc := make(chan os.Signal, 1)
	signal.Notify(ctrlc, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctrlc
		// Close flushes a final snapshot before stopping the HTTP server.
		server.Close()
	}()

	slog.Info("listening", "port", port)
	err = server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		slog.Error("server closed", "error", err)
	} else {
		slog.Info("server closed", "error", err)
	}
}
