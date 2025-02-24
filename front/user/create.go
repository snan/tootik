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

package user

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/dimkr/tootik/ap"
	"github.com/dimkr/tootik/cfg"
	"github.com/dimkr/tootik/fed/icon"
)

func gen(ctx context.Context) ([]byte, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	var privPem bytes.Buffer
	if err := pem.Encode(
		&privPem,
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(priv),
		},
	); err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	var pubPem bytes.Buffer
	if err := pem.Encode(
		&pubPem,
		&pem.Block{
			Type:  "RSA PUBLIC KEY",
			Bytes: x509.MarshalPKCS1PublicKey(&priv.PublicKey),
		},
	); err != nil {
		return nil, nil, fmt.Errorf("failed to generate public key: %w", err)
	}

	return privPem.Bytes(), pubPem.Bytes(), nil
}

func Create(ctx context.Context, db *sql.DB, id, name, certHash string) (*ap.Actor, error) {
	priv, pub, err := gen(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key pair: %w", err)
	}

	actor := ap.Actor{
		Context: []string{
			"https://www.w3.org/ns/activitystreams",
			"https://w3id.org/security/v1",
		},
		ID:                id,
		Type:              ap.Person,
		PreferredUsername: name,
		Icon: ap.Attachment{
			Type:      ap.ImageAttachment,
			MediaType: icon.MediaType,
			URL:       fmt.Sprintf("https://%s/icon/%s%s", cfg.Domain, name, icon.FileNameExtension),
		},
		Inbox:  fmt.Sprintf("https://%s/inbox/%s", cfg.Domain, name),
		Outbox: fmt.Sprintf("https://%s/outbox/%s", cfg.Domain, name),
		// use nobody's inbox as a shared inbox
		Endpoints: map[string]string{
			"sharedInbox": fmt.Sprintf("https://%s/inbox/nobody", cfg.Domain),
		},
		Followers: fmt.Sprintf("https://%s/followers/%s", cfg.Domain, name),
		PublicKey: ap.PublicKey{
			ID:           fmt.Sprintf("https://%s/user/%s#main-key", cfg.Domain, name),
			Owner:        id,
			PublicKeyPem: string(pub),
		},
		ManuallyApprovesFollowers: false,
	}

	body, err := json.Marshal(actor)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s: %w", id, err)
	}

	if _, err = db.ExecContext(
		ctx,
		`INSERT INTO persons (id, hash, actor, privkey, certhash) VALUES(?,?,?,?,?)`,
		id,
		fmt.Sprintf("%x", sha256.Sum256([]byte(id))),
		string(body),
		string(priv),
		certHash,
	); err != nil {
		return nil, fmt.Errorf("failed to insert %s: %w", id, err)
	}

	return &actor, nil
}
