package code

import (
	"errors"
	"strings"
	"testing"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

var alice = asset.Actor{Subject: "sub-alice", DisplayName: "alice@example.com"}

func newSvc() *Service { return NewService(NewMemoryStore(), 64) }

func createInput(name, version string) CreateInput {
	return CreateInput{
		EntryInput:   EntryInput{Name: name, Description: "d", Language: "Python"},
		VersionInput: VersionInput{Version: version, Source: "def f(): pass"},
	}
}

func TestService_CreatePublishesTheFirstVersion(t *testing.T) {
	svc := newSvc()
	e, err := svc.Create(ctx, "acme", alice, createInput("f", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Owner != "alice@example.com" || e.CreatedBy != "sub-alice" || e.Language != "python" {
		t.Errorf("entry = %+v (owner defaults to the publisher, language is normalized)", e)
	}
	if e.VersionCount != 1 || e.LatestVersion == nil || e.LatestVersion.Version != "1.0.0" || e.LatestVersion.PublishedBy != "alice@example.com" {
		t.Errorf("first version = count %d %+v", e.VersionCount, e.LatestVersion)
	}
}

func TestService_CreateValidatesBeforeStoring(t *testing.T) {
	svc := newSvc()
	if _, err := svc.Create(ctx, "acme", alice, CreateInput{}); fieldOf(t, err) != "name" {
		t.Errorf("empty: %v", err)
	}
	in := createInput("f", "latest")
	if _, err := svc.Create(ctx, "acme", alice, in); fieldOf(t, err) != "version" {
		t.Errorf("reserved label: %v", err)
	}
	in = createInput("f", "1")
	in.Source = strings.Repeat("x", 65) // the service was built with a 64-byte cap
	if _, err := svc.Create(ctx, "acme", alice, in); fieldOf(t, err) != "source" {
		t.Errorf("oversized source: %v", err)
	}
	if _, err := svc.Create(ctx, "Bad Workspace", alice, createInput("f", "1")); fieldOf(t, err) != "workspace" {
		t.Errorf("bad workspace: %v", err)
	}
	if page, _ := svc.List(ctx, "acme", ListFilter{}); page.Total != 0 {
		t.Errorf("a rejected create stored %d entries", page.Total)
	}
	if _, err := svc.Create(ctx, "acme", alice, createInput("f", "1")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "acme", alice, createInput("f", "1")); !errors.Is(err, asset.ErrExists) {
		t.Errorf("duplicate name: %v", err)
	}
}

func TestService_PublishAndLatest(t *testing.T) {
	svc := newSvc()
	e, _ := svc.Create(ctx, "acme", alice, createInput("f", "1.0.0"))
	bob := asset.Actor{Subject: "sub-bob", DisplayName: "bob"}

	v, err := svc.Publish(ctx, "acme", bob, e.ID, VersionInput{Version: "1.1.0", Source: "def f(): return 2", Notes: "faster"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Seq != 2 || v.PublishedBy != "bob" || v.Notes != "faster" {
		t.Errorf("published = %+v", v)
	}

	latest, err := svc.GetVersion(ctx, "acme", e.ID, LatestVersion)
	if err != nil || latest.Version != "1.1.0" || latest.Source != "def f(): return 2" {
		t.Errorf("latest = %+v, %v", latest, err)
	}
	pinned, err := svc.GetVersion(ctx, "acme", e.ID, "1.0.0")
	if err != nil || pinned.Source != "def f(): pass" {
		t.Errorf("pinned = %+v, %v", pinned, err)
	}

	if _, err := svc.Publish(ctx, "acme", bob, e.ID, VersionInput{Version: "1.1.0", Source: "x"}); !errors.Is(err, asset.ErrExists) {
		t.Errorf("republishing a label: %v, want ErrExists", err)
	}
	if _, err := svc.Publish(ctx, "acme", bob, "ghost", VersionInput{Version: "1", Source: "x"}); !errors.Is(err, asset.ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
	if _, err := svc.Publish(ctx, "acme", bob, e.ID, VersionInput{Version: "2", Source: ""}); fieldOf(t, err) != "source" {
		t.Errorf("blank source: %v", err)
	}
	if _, err := svc.GetVersion(ctx, "acme", "ghost", LatestVersion); !errors.Is(err, asset.ErrNotFound) {
		t.Errorf("latest of an unknown entry: %v", err)
	}
}

func TestService_UpdateKeepsOwnerUnlessGivenOne(t *testing.T) {
	svc := newSvc()
	in := createInput("f", "1")
	in.Owner = "team-data"
	e, _ := svc.Create(ctx, "acme", alice, in)

	upd, err := svc.Update(ctx, "acme", e.ID, EntryInput{Name: "f", Description: "better docs"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Owner != "team-data" || upd.Description != "better docs" || upd.VersionCount != 1 {
		t.Errorf("after update: %+v", upd)
	}
	if upd, err = svc.Update(ctx, "acme", e.ID, EntryInput{Name: "f", Owner: "carol"}); err != nil || upd.Owner != "carol" {
		t.Errorf("explicit reassignment: %+v, %v", upd, err)
	}
	if _, err := svc.Update(ctx, "acme", "ghost", EntryInput{Name: "f"}); !errors.Is(err, asset.ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
}

func TestService_DefaultSourceLimit(t *testing.T) {
	if got := NewService(NewMemoryStore(), 0).MaxSourceBytes(); got != DefaultMaxSourceBytes {
		t.Errorf("default limit = %d, want %d", got, DefaultMaxSourceBytes)
	}
}
