package mongo_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"microservicio-go/domain"
	mongorepo "microservicio-go/infrastructure/mongo"
)

const testCollection = "extractions_test"

func mongoEnv(t *testing.T) (uri, db, col string) {
	t.Helper()
	uri = os.Getenv("MONGODB_URI")
	if uri == "" {
		t.Skip("MONGODB_URI not set; skipping mongo integration tests")
	}
	db = os.Getenv("MONGODB_DB")
	if db == "" {
		db = "pdf_extraction"
	}
	return uri, db, testCollection
}

func dropCollection(t *testing.T, uri, db, col string) {
	t.Helper()
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect for cleanup: %v", err)
	}
	defer func() { _ = client.Disconnect(ctx) }()
	if err := client.Database(db).Collection(col).Drop(ctx); err != nil {
		t.Fatalf("drop test collection: %v", err)
	}
}

func rawClient(t *testing.T, uri string) *mongo.Client {
	t.Helper()
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetBSONOptions(&options.BSONOptions{ObjectIDAsHexString: true}))
	if err != nil {
		t.Fatalf("raw connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	return client
}

func TestRepository_RoundTripPersistsFullRecord(t *testing.T) {
	uri, db, col := mongoEnv(t)
	dropCollection(t, uri, db, col)

	ctx := context.Background()
	repo, err := mongorepo.NewRepository(ctx, uri, db, col)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	defer func() { _ = repo.Disconnect(context.Background()) }()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	rec := &domain.ExtractionRecord{
		Filename:      "roundtrip.pdf",
		MimeType:      "application/pdf",
		FileSizeBytes: 1234,
		PageCount:     2,
		TextLength:    42,
		DurationMS:    5,
		SHA256:        strings.Repeat("ab", 32),
		Status:        domain.StatusSuccess,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := repo.Save(ctx, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	client := rawClient(t, uri)
	var got domain.ExtractionRecord
	if err := client.Database(db).Collection(col).FindOne(ctx, bson.M{"sha256": rec.SHA256}).Decode(&got); err != nil {
		t.Fatalf("read back record: %v", err)
	}

	if got.Filename != "roundtrip.pdf" || got.Status != domain.StatusSuccess {
		t.Errorf("persisted record mismatch: %+v", got)
	}
	if got.MimeType != "application/pdf" || got.FileSizeBytes != 1234 {
		t.Errorf("persisted metadata mismatch: %+v", got)
	}
	if got.TextLength != 42 || got.DurationMS != 5 || got.PageCount != 2 {
		t.Errorf("persisted counters mismatch: %+v", got)
	}
	if got.Error != nil {
		t.Errorf("persisted error facet must be nil on success: %+v", got.Error)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("persisted timestamps missing: %+v", got)
	}
	if got.ID == "" {
		t.Errorf("persisted record must carry its Mongo id: %+v", got)
	}
}

func TestRepository_SaveErrorRecord_PersistsErrorFacet(t *testing.T) {
	uri, db, col := mongoEnv(t)
	dropCollection(t, uri, db, col)

	ctx := context.Background()
	repo, err := mongorepo.NewRepository(ctx, uri, db, col)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	defer func() { _ = repo.Disconnect(context.Background()) }()

	now := time.Now().UTC()
	rec := &domain.ExtractionRecord{
		Filename:  "failed.pdf",
		Status:    domain.StatusError,
		Error:     &domain.ErrorDetails{Type: domain.ErrorTypeMalformedPDF, Message: "open document: malformed document"},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repo.Save(ctx, rec); err != nil {
		t.Fatalf("Save error record: %v", err)
	}

	client := rawClient(t, uri)
	var got domain.ExtractionRecord
	if err := client.Database(db).Collection(col).FindOne(ctx, bson.M{"filename": "failed.pdf"}).Decode(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Status != domain.StatusError {
		t.Errorf("status = %q, want %q", got.Status, domain.StatusError)
	}
	if got.Error == nil || got.Error.Type != domain.ErrorTypeMalformedPDF || got.Error.Message == "" {
		t.Errorf("persisted error facet mismatch: %+v", got.Error)
	}
}

func TestRepository_Ping(t *testing.T) {
	uri, db, col := mongoEnv(t)
	ctx := context.Background()
	repo, err := mongorepo.NewRepository(ctx, uri, db, col)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	defer func() { _ = repo.Disconnect(context.Background()) }()
	if err := repo.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestRepository_EnforcesCreatedAtIndex(t *testing.T) {
	uri, db, col := mongoEnv(t)
	dropCollection(t, uri, db, col)

	ctx := context.Background()
	repo, err := mongorepo.NewRepository(ctx, uri, db, col)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	defer func() { _ = repo.Disconnect(context.Background()) }()

	client := rawClient(t, uri)
	cursor, err := client.Database(db).Collection(col).Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	found := false
	for cursor.Next(ctx) {
		var idx bson.M
		if err := cursor.Decode(&idx); err != nil {
			t.Fatalf("decode index: %v", err)
		}
		if idx["name"] == "created_at_desc" {
			found = true
		}
	}
	if !found {
		t.Error("index created_at_desc not found")
	}
}

func TestRepository_FailsFastOnUnreachableInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	repo, err := mongorepo.NewRepository(ctx, "mongodb://127.0.0.1:59999", "", "")
	if err == nil {
		if repo != nil {
			_ = repo.Disconnect(context.Background())
		}
		t.Fatal("expected an error for an unreachable instance")
	}
}
