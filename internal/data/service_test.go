package data

import (
	"errors"
	"testing"

	"github.com/projectbooth/booth-catalog/internal/asset"
)

var alice = asset.Actor{Subject: "sub-alice", DisplayName: "alice@example.com"}

func newSvc() *Service { return NewService(NewMemoryStore()) }

func TestService_CreateDefaultsOwnerToTheActor(t *testing.T) {
	svc := newSvc()
	in := validInput()

	d, err := svc.Create(ctx, "acme", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Owner != "alice@example.com" {
		t.Errorf("Owner = %q, want it defaulted to the registering user (ADR 0042)", d.Owner)
	}
	if d.CreatedBy != "sub-alice" {
		t.Errorf("CreatedBy = %q, want the token subject", d.CreatedBy)
	}
	if d.ID == "" || d.CreatedAt.IsZero() || !d.CreatedAt.Equal(d.UpdatedAt) {
		t.Errorf("ID/timestamps not assigned: %+v", d)
	}

	// An explicit owner (a person or a team) is kept, and is not the same thing as CreatedBy.
	in.Name, in.Owner = "other", "team-finance"
	d2, err := svc.Create(ctx, "acme", alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Owner != "team-finance" || d2.CreatedBy != "sub-alice" {
		t.Errorf("Owner=%q CreatedBy=%q", d2.Owner, d2.CreatedBy)
	}
}

func TestService_CreateValidates(t *testing.T) {
	svc := newSvc()
	if _, err := svc.Create(ctx, "acme", alice, Input{}); fieldOf(t, err) != "name" {
		t.Errorf("empty input: %v", err)
	}
	if _, err := svc.Create(ctx, "Not A Slug", alice, validInput()); fieldOf(t, err) != "workspace" {
		t.Errorf("bad workspace: %v", err)
	}
	if _, err := svc.Create(ctx, "acme", alice, validInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "acme", alice, validInput()); !errors.Is(err, asset.ErrExists) {
		t.Errorf("duplicate name: %v, want ErrExists", err)
	}
}

// An editor fixing a typo must not silently take ownership of someone else's dataset.
func TestService_UpdateKeepsTheOwnerUnlessGivenOne(t *testing.T) {
	svc := newSvc()
	in := validInput()
	in.Owner = "bob"
	d, err := svc.Create(ctx, "acme", alice, in)
	if err != nil {
		t.Fatal(err)
	}

	in.Description, in.Owner = "fixed a typo", ""
	upd, err := svc.Update(ctx, "acme", d.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if upd.Owner != "bob" || upd.Description != "fixed a typo" {
		t.Errorf("after update: %+v", upd)
	}
	if upd.CreatedBy != d.CreatedBy || !upd.CreatedAt.Equal(d.CreatedAt) {
		t.Error("update rewrote provenance")
	}
	if !upd.UpdatedAt.After(d.UpdatedAt) && !upd.UpdatedAt.Equal(d.UpdatedAt) {
		t.Errorf("UpdatedAt went backwards: %v -> %v", d.UpdatedAt, upd.UpdatedAt)
	}

	in.Owner = "carol"
	if upd, err = svc.Update(ctx, "acme", d.ID, in); err != nil || upd.Owner != "carol" {
		t.Errorf("explicit reassignment: %+v, %v", upd, err)
	}

	got, _ := svc.Get(ctx, "acme", d.ID)
	if got.Owner != "carol" {
		t.Errorf("stored owner = %q", got.Owner)
	}
}

func TestService_UpdateErrors(t *testing.T) {
	svc := newSvc()
	if _, err := svc.Update(ctx, "acme", "ghost", validInput()); !errors.Is(err, asset.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if _, err := svc.Update(ctx, "acme", "ghost", Input{}); fieldOf(t, err) != "name" {
		t.Errorf("invalid input is reported before existence: %v", err)
	}
}

func TestService_ListNormalizesTagFilters(t *testing.T) {
	svc := newSvc()
	in := validInput()
	in.Tags = []string{"PII"}
	if _, err := svc.Create(ctx, "acme", alice, in); err != nil {
		t.Fatal(err)
	}
	page, err := svc.List(ctx, "acme", ListFilter{Tags: []string{" Pii "}})
	if err != nil || page.Total != 1 {
		t.Errorf("filter for \" Pii \" = %+v, %v; want the dataset tagged PII", page, err)
	}
	if _, err := svc.List(ctx, "acme", ListFilter{Tags: []string{"two words"}}); fieldOf(t, err) != "tags" {
		t.Errorf("malformed tag filter: %v", err)
	}
}

func TestService_ContainingLocationNormalizes(t *testing.T) {
	svc := newSvc()
	if _, err := svc.Create(ctx, "acme", alice, validInput()); err != nil { // lake:warehouse/orders
		t.Fatal(err)
	}
	ds, err := svc.ContainingLocation(ctx, "acme", asset.Location{BackendID: "lake", Path: "warehouse/orders/2026/"})
	if err != nil || len(ds) != 1 {
		t.Errorf("ContainingLocation = %v, %v", ds, err)
	}
	if _, err := svc.ContainingLocation(ctx, "acme", asset.Location{BackendID: "lake", Path: "../x"}); err == nil {
		t.Error("accepted a traversing path")
	}
}
