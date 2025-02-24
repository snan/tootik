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
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/dimkr/tootik/cfg"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

type webFingerHandler struct {
	Log *slog.Logger
	DB  *sql.DB
}

func (h *webFingerHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	if len(query) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No query"))
		return
	}

	resource, err := url.QueryUnescape(query.Get("resource"))
	if err != nil {
		h.Log.Info("Failed to decode query", "resource", resource, "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	resource = strings.TrimPrefix(resource, "acct:")

	var username string

	prefix := fmt.Sprintf("https://%s/", cfg.Domain)
	if strings.HasPrefix(resource, prefix) {
		username = filepath.Base(resource)
	} else {
		var fields = strings.Split(resource, "@")

		if len(fields) > 2 {
			h.Log.Info("Received invalid resource", "resource", resource)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Resource must contain zero or one @"))
			return
		}

		if len(fields) == 2 && fields[1] != cfg.Domain {
			h.Log.Info("Received invalid resource", "resource", resource, "domain", fields[1])
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "Resource must end with @%s", cfg.Domain)
			return
		}

		username = fields[0]
	}

	id := fmt.Sprintf("https://%s/user/%s", cfg.Domain, username)
	h.Log.Info("Looking up resource", "resource", resource, "id", id)

	var exists int
	if err := h.DB.QueryRowContext(r.Context(), `select exists (select 1 from persons where id = ?)`, id).Scan(&exists); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if exists == 0 {
		h.Log.Info("Notifying that user does not exist", "user", id)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	j, err := json.Marshal(map[string]any{
		"subject": fmt.Sprintf("acct:%s@%s", username, cfg.Domain),
		"aliases": []string{id},
		"links": []map[string]any{
			{
				"rel":  "self",
				"type": "application/activity+json",
				"href": id,
			},
			{
				"rel":  "self",
				"type": `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`,
				"href": id,
			},
		},
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/jrd+json; charset=utf-8")
	w.Write(j)
}
