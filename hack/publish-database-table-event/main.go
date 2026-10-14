// Command publish-database-table-event puts one database.table.created / updated / deleted
// event on the event bus, in the provisional shape ADR 0116
// (booth-architecture/decisions/0116-workspace-database-tables-in-catalog-with-preview-and-sql.md)
// describes. It is dev tooling, mirroring hack/publish-dashboard-event: what booth-database will
// do for real, done by hand, so the catalog's database-table handling can be built and
// demonstrated before booth-database's own publisher exists.
//
//	go run ./hack/publish-database-table-event -url nats://localhost:4222 -workspace acme \
//	    -type created -schema nfl_data -name players -kind table -approx-rows 1696 \
//	    -columns '[{"name":"player_id","type":"bigint","nullable":false},{"name":"name","type":"text","nullable":true,"comment":"display name"}]'
//
// With -create-stream it first creates the BOOTH_EVENTS stream the way booth-core does (so this
// works against a bare NATS server); without it, the stream must already exist.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	var (
		url          = flag.String("url", "nats://localhost:4222", "NATS server")
		workspace    = flag.String("workspace", "acme", "workspace slug")
		eventType    = flag.String("type", "created", "created, updated or deleted")
		module       = flag.String("module", "database", "publishing module id (the envelope's publishedBy)")
		schema       = flag.String("schema", "", "Postgres schema name (required)")
		name         = flag.String("name", "", "Postgres table name (required)")
		kind         = flag.String("kind", "table", "table, view or materialized view (required for created/updated)")
		columns      = flag.String("columns", "[]", `JSON array of {"name","type","nullable","comment"}`)
		approxRows   = flag.Int64("approx-rows", -1, "approximate row count; omit (leave at -1) for unknown/null")
		schemaHash   = flag.String("schema-hash", "", "booth-database's own change-detection key — carried on the wire but never read by the catalog")
		at           = flag.String("at", "", "publishedAt as RFC 3339 (default: now)")
		createStream = flag.Bool("create-stream", false, "create the BOOTH_EVENTS stream if missing")
	)
	flag.Parse()
	if *schema == "" || *name == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *eventType != "created" && *eventType != "updated" && *eventType != "deleted" {
		log.Fatalf("-type must be created, updated or deleted, got %q", *eventType)
	}

	var cols []map[string]any
	if err := json.Unmarshal([]byte(*columns), &cols); err != nil {
		log.Fatalf("-columns is not a JSON array: %v", err)
	}
	published := time.Now().UTC()
	if *at != "" {
		var err error
		if published, err = time.Parse(time.RFC3339Nano, *at); err != nil {
			log.Fatalf("-at: %v", err)
		}
	}

	data := map[string]any{"schema": *schema, "name": *name}
	if *eventType != "deleted" {
		data["kind"], data["columns"] = *kind, cols
		if *approxRows >= 0 {
			data["approxRows"] = *approxRows
		} else {
			data["approxRows"] = nil
		}
		if *schemaHash != "" {
			data["schemaHash"] = *schemaHash
		}
	}
	fullType := "database.table." + *eventType
	payload, err := json.Marshal(map[string]any{
		"workspace": *workspace, "eventType": fullType, "publishedAt": published.Format(time.RFC3339Nano),
		"publishedBy": *module, "data": data,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nc, err := nats.Connect(*url, nats.Name("booth-catalog-dev-publisher"))
	if err != nil {
		log.Fatalf("connecting to %s: %v", *url, err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatal(err)
	}
	if *createStream {
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: "BOOTH_EVENTS", Subjects: []string{"booth.>"}, Storage: jetstream.FileStorage, MaxAge: 7 * 24 * time.Hour}); err != nil {
			log.Fatalf("creating stream: %v", err)
		}
	}
	subject := fmt.Sprintf("booth.%s.%s", *workspace, fullType)
	ack, err := js.Publish(ctx, subject, payload)
	if err != nil {
		log.Fatalf("publishing to %s: %v", subject, err)
	}
	fmt.Printf("published %s (stream %s, seq %d)\n%s\n", subject, ack.Stream, ack.Sequence, payload)
}
