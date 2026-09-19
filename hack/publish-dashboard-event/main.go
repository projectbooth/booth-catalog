// Command publish-dashboard-event puts one dashboard.created / updated / deleted event on the
// event bus, in the shape docs/decisions/0001-dashboard-event-payload.md proposes. It is dev
// tooling: what a dashboard module will do for real, done by hand, so the catalog's dashboard
// views can be developed and demonstrated before booth-superset / booth-metabase /
// booth-streamlit exist (the brief's "mocked event stream").
//
//	go run ./hack/publish-dashboard-event -url nats://localhost:4222 -workspace acme \
//	    -type created -module superset -id 42 -name "Revenue" -owner alice \
//	    -path /superset/dashboard/42 \
//	    -sources '[{"type":"location","backendId":"lake","path":"warehouse/orders"}]'
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
		module       = flag.String("module", "superset", "publishing module id (the envelope's publishedBy)")
		id           = flag.String("id", "", "dashboard ID inside the publishing tool (required)")
		name         = flag.String("name", "", "dashboard name (required for created/updated)")
		description  = flag.String("description", "", "description")
		owner        = flag.String("owner", "", "owner")
		path         = flag.String("path", "", "shell-relative path that opens the dashboard")
		complete     = flag.Bool("lineage-complete", false, "assert that -sources lists everything the dashboard reads")
		sources      = flag.String("sources", "[]", "JSON array of lineage sources")
		at           = flag.String("at", "", "publishedAt as RFC 3339 (default: now)")
		createStream = flag.Bool("create-stream", false, "create the BOOTH_EVENTS stream if missing")
	)
	flag.Parse()
	if *id == "" || (*eventType != "deleted" && *name == "") {
		flag.Usage()
		os.Exit(2)
	}
	if *eventType != "created" && *eventType != "updated" && *eventType != "deleted" {
		log.Fatalf("-type must be created, updated or deleted, got %q", *eventType)
	}

	var srcs []map[string]any
	if err := json.Unmarshal([]byte(*sources), &srcs); err != nil {
		log.Fatalf("-sources is not a JSON array: %v", err)
	}
	published := time.Now().UTC()
	if *at != "" {
		var err error
		if published, err = time.Parse(time.RFC3339Nano, *at); err != nil {
			log.Fatalf("-at: %v", err)
		}
	}

	data := map[string]any{"dashboardId": *id}
	if *eventType != "deleted" {
		data["name"], data["description"], data["owner"], data["path"] = *name, *description, *owner, *path
		data["lineageComplete"], data["sources"] = *complete, srcs
	}
	fullType := "dashboard." + *eventType
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
