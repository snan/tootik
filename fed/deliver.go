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

package fed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dimkr/tootik/ap"
	"github.com/dimkr/tootik/cfg"
	"github.com/dimkr/tootik/data"
	"log/slog"
	"strings"
	"time"
)

const (
	batchSize             = 16
	deliveryRetryInterval = int64((time.Hour / 2) / time.Second)
	MaxDeliveryAttempts   = 5
	pollingInterval       = time.Second * 5
	deliveryTimeout       = time.Minute * 5
)

func ProcessQueue(ctx context.Context, log *slog.Logger, db *sql.DB, resolver *Resolver) {
	t := time.NewTicker(pollingInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-t.C:
			if err := processQueue(ctx, log, db, resolver); err != nil {
				log.Error("Failed to deliver posts", "error", err)
			}
		}
	}
}

func processQueue(ctx context.Context, log *slog.Logger, db *sql.DB, resolver *Resolver) error {
	log.Debug("Polling delivery queue")

	rows, err := db.QueryContext(ctx, `select outbox.attempts, outbox.activity, persons.actor from outbox join persons on persons.id = outbox.sender where outbox.sent = 0 and (outbox.attempts = 0 or (outbox.attempts < ? and outbox.last <= unixepoch() - ?)) order by outbox.attempts asc, outbox.last asc limit ?`, MaxDeliveryAttempts, deliveryRetryInterval, batchSize)
	if err != nil {
		return fmt.Errorf("failed to fetch posts to deliver: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var activityString, actorString string
		var deliveryAttempts int
		if err := rows.Scan(&deliveryAttempts, &activityString, &actorString); err != nil {
			log.Error("Failed to fetch post to deliver", "error", err)
			continue
		}

		var activity ap.Activity
		if err := json.Unmarshal([]byte(activityString), &activity); err != nil {
			log.Error("Failed to unmarshal undelivered activity", "attempts", deliveryAttempts, "error", err)
			continue
		}

		if _, err := db.ExecContext(ctx, `update outbox set last = unixepoch(), attempts = ? where activity->>'id' = ?`, deliveryAttempts+1, activity.ID); err != nil {
			log.Error("Failed to save last delivery attempt time", "id", activity.ID, "attempts", deliveryAttempts, "error", err)
			continue
		}

		var actor ap.Actor
		if err := json.Unmarshal([]byte(actorString), &actor); err != nil {
			log.Error("Failed to unmarshal undelivered activity actor", "id", activity.ID, "attempts", deliveryAttempts, "error", err)
			continue
		}

		if err := deliverWithTimeout(ctx, log, db, resolver, &activity, []byte(activityString), &actor); err != nil {
			log.Warn("Failed to deliver activity", "id", activity.ID, "attempts", deliveryAttempts, "error", err)
			continue
		}

		if _, err := db.ExecContext(ctx, `update outbox set sent = 1 where activity->>'id' = ?`, activity.ID); err != nil {
			log.Error("Failed to mark delivery as completed", "id", activity.ID, "attempts", deliveryAttempts, "error", err)
			continue
		}

		log.Info("Successfully delivered an activity", "id", activity.ID, "attempts", deliveryAttempts)
	}

	return nil
}

func deliverWithTimeout(parent context.Context, log *slog.Logger, db *sql.DB, resolver *Resolver, activity *ap.Activity, rawActivity []byte, actor *ap.Actor) error {
	ctx, cancel := context.WithTimeout(parent, deliveryTimeout)
	defer cancel()
	return deliver(ctx, log, db, activity, rawActivity, actor, resolver)
}

func deliver(ctx context.Context, log *slog.Logger, db *sql.DB, activity *ap.Activity, rawActivity []byte, actor *ap.Actor, resolver *Resolver) error {
	recipients := data.OrderedMap[string, struct{}]{}

	// deduplicate recipients or skip if we're forwarding an activity
	if activity.Actor == actor.ID {
		activity.To.Range(func(id string, _ struct{}) bool {
			recipients.Store(id, struct{}{})
			return true
		})

		activity.CC.Range(func(id string, _ struct{}) bool {
			recipients.Store(id, struct{}{})
			return true
		})
	}

	actorIDs := data.OrderedMap[string, struct{}]{}
	wideDelivery := activity.Actor != actor.ID || activity.IsPublic() || recipients.Contains(actor.Followers)

	// list the actor's federated followers if we're forwarding an activity by another actor, or if addressed by actor
	if wideDelivery {
		followers, err := db.QueryContext(ctx, `select distinct follower from follows where followed = ? and follower not like ? and accepted = 1`, actor.ID, fmt.Sprintf("https://%s/%%", cfg.Domain))
		if err != nil {
			log.Warn("Failed to list followers", "activity", activity.ID, "error", err)
		} else {
			for followers.Next() {
				var follower string
				if err := followers.Scan(&follower); err != nil {
					log.Warn("Skipped a follower", "activity", activity.ID, "error", err)
					continue
				}

				actorIDs.Store(follower, struct{}{})
			}

			followers.Close()
		}
	}

	// assume that all other federated recipients are actors and not collections
	recipients.Range(func(recipient string, _ struct{}) bool {
		actorIDs.Store(recipient, struct{}{})
		return true
	})

	anyFailed := false

	var author string
	if obj, ok := activity.Object.(*ap.Object); ok {
		author = obj.AttributedTo
	}

	prefix := fmt.Sprintf("https://%s/", cfg.Domain)

	sent := map[string]struct{}{}

	actorIDs.Range(func(actorID string, _ struct{}) bool {
		if actorID == author || actorID == ap.Public || strings.HasPrefix(actorID, prefix) {
			log.Debug("Skipping recipient", "to", actorID, "activity", activity.ID)
			return true
		}

		to, err := resolver.Resolve(ctx, log, db, actor, actorID, false)
		if err != nil {
			log.Warn("Failed to resolve a recipient", "to", actorID, "activity", activity.ID, "error", err)
			if !errors.Is(err, ErrActorGone) && !errors.Is(err, ErrBlockedDomain) {
				anyFailed = true
			}
			return true
		}

		// if possible, use the recipients's shared inbox and skip other recipients with the same shared inbox
		inbox := to.Inbox
		if wideDelivery {
			if sharedInbox, ok := to.Endpoints["sharedInbox"]; ok && sharedInbox != "" {
				log.Debug("Using shared inbox inbox", "to", actorID, "activity", activity.ID, "shared_inbox", inbox)
				inbox = sharedInbox
			}
		}

		if _, ok := sent[inbox]; ok {
			log.Info("Skipping recipient with shared inbox", "to", actorID, "activity", activity.ID, "inbox", inbox)
			return true
		}

		sent[inbox] = struct{}{}

		log.Info("Delivering activity to recipient", "to", actorID, "inbox", inbox, "activity", activity.ID)

		if err := Send(ctx, log, db, actor, resolver, inbox, rawActivity); err != nil {
			log.Warn("Failed to send an activity", "to", actorID, "activity", activity.ID, "error", err)
			if !errors.Is(err, ErrBlockedDomain) {
				anyFailed = true
			}
			return true
		}

		return true
	})

	if anyFailed {
		return fmt.Errorf("failed to deliver activity %s to at least one recipient", activity.ID)
	}

	return nil
}
