package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"microservicio-go/domain"
)

var _ domain.ExtractionRepository = (*Repository)(nil)

const indexCreatedAt = "created_at_desc"

// Repository persists ExtractionRecords in MongoDB. It owns a single client
// (connection pooling handled by the driver) and exposes Ping for health and
// Disconnect for graceful shutdown.
type Repository struct {
	client     *mongo.Client
	collection *mongo.Collection
}

// NewRepository connects once, fails fast when the instance is unreachable,
// and ensures the created_at index exists. The caller must call Disconnect on
// shutdown.
func NewRepository(ctx context.Context, uri, db, collection string) (*Repository, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetConnectTimeout(5 * time.Second).SetServerSelectionTimeout(5 * time.Second))
	if err != nil {
		return nil, fmt.Errorf("connect mongodb: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mongodb: %w", err)
	}

	repo := &Repository{
		client:     client,
		collection: client.Database(db).Collection(collection),
	}
	if err := repo.ensureIndex(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ensure index: %w", err)
	}
	return repo, nil
}

func (r *Repository) Save(ctx context.Context, record *domain.ExtractionRecord) error {
	if _, err := r.collection.InsertOne(ctx, record); err != nil {
		return fmt.Errorf("insert extraction record: %w", err)
	}
	return nil
}

func (r *Repository) Ping(ctx context.Context) error {
	if err := r.client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("ping mongodb: %w", err)
	}
	return nil
}

func (r *Repository) Disconnect(ctx context.Context) error {
	if err := r.client.Disconnect(ctx); err != nil {
		return fmt.Errorf("disconnect mongodb: %w", err)
	}
	return nil
}

// ensureIndex creates a descending index on created_at; creating an identical
// index is idempotent.
func (r *Repository) ensureIndex(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "created_at", Value: -1}},
		Options: options.Index().SetName(indexCreatedAt),
	})
	if err != nil {
		return fmt.Errorf("create index %s: %w", indexCreatedAt, err)
	}
	return nil
}
