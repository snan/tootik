/*
Copyright 2023 Dima Krasner

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package outbox

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dimkr/tootik/ap"
	"github.com/dimkr/tootik/cfg"
	"github.com/dimkr/tootik/fed"
	"github.com/dimkr/tootik/inbox/note"
	"log/slog"
	"time"
)

const maxDeliveryQueueSize = 128

var ErrDeliveryQueueFull = errors.New("delivery queue is full")

func Create(ctx context.Context, log *slog.Logger, db *sql.DB, post *ap.Object, author *ap.Actor) error {
	var queueSize int
	if err := db.QueryRowContext(ctx, `select count(*) from outbox where sent = 0 and attempts < ?`, fed.MaxDeliveryAttempts).Scan(&queueSize); err != nil {
		return fmt.Errorf("failed to query delivery queue size: %w", err)
	}

	if queueSize >= maxDeliveryQueueSize {
		return ErrDeliveryQueueFull
	}

	create, err := json.Marshal(ap.Activity{
		Context: "https://www.w3.org/ns/activitystreams",
		Type:    ap.CreateActivity,
		ID:      fmt.Sprintf("https://%s/create/%x", cfg.Domain, sha256.Sum256([]byte(fmt.Sprintf("%s|%d", post.ID, time.Now().Unix())))),
		Actor:   author.ID,
		Object:  post,
		To:      post.To,
		CC:      post.CC,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal Create: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err := note.Insert(ctx, log, tx, post); err != nil {
		return fmt.Errorf("failed to insert note: %w", err)
	}

	if _, err = tx.ExecContext(ctx, `insert into outbox (activity, sender) values(?,?)`, string(create), author.ID); err != nil {
		return fmt.Errorf("failed to insert Create: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to create note: %w", err)
	}

	return nil
}
