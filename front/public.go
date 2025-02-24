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

package front

import (
	"fmt"
	"github.com/dimkr/tootik/cfg"
	"github.com/dimkr/tootik/data"
	"github.com/dimkr/tootik/front/text"
)

const maxOffset = postsPerPage * 30

func local(w text.Writer, r *request) {
	offset, err := getOffset(r.URL)
	if err != nil {
		r.Log.Info("Failed to parse query", "url", r.URL, "error", err)
		w.Status(40, "Invalid query")
		return
	}

	if offset > maxOffset {
		r.Log.Warn("Offset is too big", "offset", offset)
		w.Statusf(40, "Offset must be <= %d", maxOffset)
		return
	}

	rows, err := r.Query(`select notes.object, persons.actor from notes left join (select object->>'inReplyTo' as id, count(*) as count from notes where inserted > unixepoch()-60*60*24*7 group by object->>'inReplyTo') replies on notes.id = replies.id join persons on notes.author = persons.id left join (select author, max(inserted) as last, count(*)/(60*60*24) as avg from notes where inserted > unixepoch()-60*60*24*7 group by author) stats on notes.author = stats.author where notes.public = 1 and notes.host = $1 order by notes.inserted / 86400 desc, replies.count desc, stats.avg asc, stats.last asc, notes.inserted desc limit $2 offset $3;`, cfg.Domain, postsPerPage, offset)
	if err != nil {
		r.Log.Warn("Failed to fetch public posts", "error", err)
		w.Error()
		return
	}
	defer rows.Close()

	notes := data.OrderedMap[string, noteMetadata]{}

	for rows.Next() {
		noteString := ""
		var meta noteMetadata
		if err := rows.Scan(&noteString, &meta.Author); err != nil {
			r.Log.Warn("Failed to scan post", "error", err)
			continue
		}

		notes.Store(noteString, meta)
	}
	rows.Close()

	count := len(notes)

	w.OK()

	if offset >= postsPerPage || count == postsPerPage {
		w.Titlef("📡 This Planet (%d-%d)", offset, offset+postsPerPage)
	} else {
		w.Title("📡 This Planet")
	}

	if count == 0 {
		w.Text("No posts.")
	} else {
		r.PrintNotes(w, notes, true, true)
	}

	if offset >= postsPerPage || count == postsPerPage {
		w.Separator()
	}

	if offset >= postsPerPage && r.User == nil {
		w.Linkf(fmt.Sprintf("/local?%d", offset-postsPerPage), "Previous page (%d-%d)", offset-postsPerPage, offset)
	} else if offset >= postsPerPage {
		w.Linkf(fmt.Sprintf("/users/local?%d", offset-postsPerPage), "Previous page (%d-%d)", offset-postsPerPage, offset)
	}

	if count == postsPerPage && offset+postsPerPage <= maxOffset && r.User == nil {
		w.Linkf(fmt.Sprintf("/local?%d", offset+postsPerPage), "Next page (%d-%d)", offset+postsPerPage, offset+2*postsPerPage)
	} else if count == postsPerPage && offset+postsPerPage <= maxOffset {
		w.Linkf(fmt.Sprintf("/users/local?%d", offset+postsPerPage), "Next page (%d-%d)", offset+postsPerPage, offset+2*postsPerPage)
	}
}

func federated(w text.Writer, r *request) {
	offset, err := getOffset(r.URL)
	if err != nil {
		r.Log.Info("Failed to parse query", "url", r.URL, "error", err)
		w.Status(40, "Invalid query")
		return
	}

	if offset > maxOffset {
		r.Log.Warn("Offset is too big", "offset", offset)
		w.Statusf(40, "Offset must be <= %d", maxOffset)
		return
	}

	rows, err := r.Query(`select notes.object, persons.actor, groups.actor from notes join persons on notes.author = persons.id left join (select author, max(inserted) as last, count(*)/(60*60*24) as avg from notes where inserted > unixepoch()-60*60*24*7 group by author) stats on notes.author = stats.author left join (select id, actor from persons where actor->>'type' = 'Group') groups on groups.id = notes.groupid where notes.public = 1 group by notes.id order by notes.inserted / 3600 desc, stats.avg asc, stats.last asc, notes.inserted desc limit $1 offset $2;`, postsPerPage, offset)
	if err != nil {
		r.Log.Warn("Failed to fetch federated posts", "error", err)
		w.Error()
		return
	}
	defer rows.Close()

	notes := data.OrderedMap[string, noteMetadata]{}

	for rows.Next() {
		noteString := ""
		var meta noteMetadata
		if err := rows.Scan(&noteString, &meta.Author, &meta.Group); err != nil {
			r.Log.Warn("Failed to scan post", "error", err)
			continue
		}

		notes.Store(noteString, meta)
	}
	rows.Close()

	count := len(notes)

	w.OK()

	if offset >= postsPerPage || count == postsPerPage {
		w.Titlef("✨️ FOMO From Outer Space (%d-%d)", offset, offset+postsPerPage)
	} else {
		w.Title("✨️ FOMO From Outer Space")
	}

	r.PrintNotes(w, notes, true, true)

	if offset >= postsPerPage || count == postsPerPage {
		w.Separator()
	}

	if offset >= postsPerPage && r.User == nil {
		w.Linkf(fmt.Sprintf("/federated?%d", offset-postsPerPage), "Previous page (%d-%d)", offset-postsPerPage, offset)
	} else if offset >= postsPerPage {
		w.Linkf(fmt.Sprintf("/users/federated?%d", offset-postsPerPage), "Previous page (%d-%d)", offset-postsPerPage, offset)
	}

	if count == postsPerPage && offset+postsPerPage <= maxOffset && r.User == nil {
		w.Linkf(fmt.Sprintf("/federated?%d", offset+postsPerPage), "Next page (%d-%d)", offset+postsPerPage, offset+2*postsPerPage)
	} else if count == postsPerPage && offset+postsPerPage <= maxOffset {
		w.Linkf(fmt.Sprintf("/users/federated?%d", offset+postsPerPage), "Next page (%d-%d)", offset+postsPerPage, offset+2*postsPerPage)
	}
}

func home(w text.Writer, r *request) {
	if r.User != nil {
		w.Redirect("/users")
		return
	}

	w.OK()
	w.Raw(logoAlt, logo)
	w.Title(cfg.Domain)
	w.Textf("Welcome, fedinaut! %s is an instance of tootik, a federated nanoblogging service.", cfg.Domain)
}
