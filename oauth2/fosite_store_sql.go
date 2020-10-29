/*
 * Copyright © 2015-2018 Aeneas Rekkas <aeneas+oss@aeneas.io>
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * @author		Aeneas Rekkas <aeneas+oss@aeneas.io>
 * @copyright 	2015-2018 Aeneas Rekkas <aeneas+oss@aeneas.io>
 * @license 	Apache-2.0
 */

package oauth2

import (
	"context"
	"crypto/sha512"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
	"github.com/tidwall/gjson"

	"github.com/ory/x/logrusx"

	"github.com/ory/herodot"

	"github.com/ory/fosite"
	"github.com/ory/x/sqlcon"
	"github.com/ory/x/stringsx"

	"github.com/ory/hydra/client"
	"github.com/ory/hydra/jwk"
)

type FositeSQLStore struct {
	DB *sqlx.DB

	r  InternalRegistry
	c  Configuration
	kc *jwk.AEAD

	HashSignature bool
}

type sqlxDB interface {
	sqlx.ExecerContext
	sqlx.Ext
	NamedExecContext(ctx context.Context, query string, arg interface{}) (sql.Result, error)
	GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
}

func NewFositeSQLStore(db *sqlx.DB, r InternalRegistry, c Configuration, kc *jwk.AEAD) *FositeSQLStore {
	return &FositeSQLStore{r: r, c: c, kc: kc, DB: db}
}

type tableName string

const (
	sqlTableOpenID         tableName = "oidc"
	sqlTableAccess         tableName = "access"
	sqlTableRefresh        tableName = "refresh"
	sqlTableCode           tableName = "code"
	sqlTablePKCE           tableName = "pkce"
	sqlTableBlacklistedJTI tableName = "jti_blacklist"
)

type transactionKey int

const txKey transactionKey = iota

var sqlParams = []string{
	"signature",
	"request_id",
	"requested_at",
	"client_id",
	"scope",
	"granted_scope",
	"form_data",
	"session_data",
	"subject",
	"active",
	"requested_audience",
	"granted_audience",
	"challenge_id",
}

type SQLData struct {
	PK                int            `db:"pk"`
	Signature         string         `db:"signature"`
	Request           string         `db:"request_id"`
	ConsentChallenge  sql.NullString `db:"challenge_id"`
	RequestedAt       time.Time      `db:"requested_at"`
	Client            string         `db:"client_id"`
	Scopes            string         `db:"scope"`
	GrantedScope      string         `db:"granted_scope"`
	RequestedAudience string         `db:"requested_audience"`
	GrantedAudience   string         `db:"granted_audience"`
	Form              string         `db:"form_data"`
	Subject           string         `db:"subject"`
	Active            bool           `db:"active"`
	Session           []byte         `db:"session_data"`
}

func sqlSchemaFromRequest(signature string, r fosite.Requester, c Configuration, kc *jwk.AEAD, logger *logrusx.Logger) (*SQLData, error) {
	subject := ""
	if r.GetSession() == nil {
		logger.Debugf("Got an empty session in sqlSchemaFromRequest")
	} else {
		subject = r.GetSession().GetSubject()
	}

	session, err := json.Marshal(r.GetSession())
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if c.EncryptSessionData() {
		ciphertext, err := kc.Encrypt(session)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		session = []byte(ciphertext)
	}

	var challenge sql.NullString
	rr, ok := r.GetSession().(*Session)
	if !ok && r.GetSession() != nil {
		return nil, errors.Errorf("Expected request to be of type *Session, but got: %T", r.GetSession())
	} else if ok {
		if len(rr.ConsentChallenge) > 0 {
			challenge = sql.NullString{Valid: true, String: rr.ConsentChallenge}
		}
	}

	return &SQLData{
		Request:           r.GetID(),
		ConsentChallenge:  challenge,
		Signature:         signature,
		RequestedAt:       r.GetRequestedAt(),
		Client:            r.GetClient().GetID(),
		Scopes:            strings.Join([]string(r.GetRequestedScopes()), "|"),
		GrantedScope:      strings.Join([]string(r.GetGrantedScopes()), "|"),
		GrantedAudience:   strings.Join([]string(r.GetGrantedAudience()), "|"),
		RequestedAudience: strings.Join([]string(r.GetRequestedAudience()), "|"),
		Form:              r.GetRequestForm().Encode(),
		Session:           session,
		Subject:           subject,
		Active:            true,
	}, nil
}

func (s *SQLData) toRequest(session fosite.Session, cm client.Manager, conf Configuration, kc *jwk.AEAD, logger *logrusx.Logger) (*fosite.Request, error) {
	sess := s.Session
	if !gjson.ValidBytes(sess) {
		var err error
		sess, err = kc.Decrypt(string(sess))
		if err != nil {
			return nil, errors.WithStack(err)
		}
	}

	if session != nil {
		if err := json.Unmarshal(sess, session); err != nil {
			return nil, errors.WithStack(err)
		}
	} else {
		logger.Debugf("Got an empty session in toRequest")
	}

	c, err := cm.GetClient(context.Background(), s.Client)
	if err != nil {
		return nil, err
	}

	val, err := url.ParseQuery(s.Form)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	r := &fosite.Request{
		ID:                s.Request,
		RequestedAt:       s.RequestedAt,
		Client:            c,
		RequestedScope:    fosite.Arguments(stringsx.Splitx(s.Scopes, "|")),
		GrantedScope:      fosite.Arguments(stringsx.Splitx(s.GrantedScope, "|")),
		RequestedAudience: fosite.Arguments(stringsx.Splitx(s.RequestedAudience, "|")),
		GrantedAudience:   fosite.Arguments(stringsx.Splitx(s.GrantedAudience, "|")),
		Form:              val,
		Session:           session,
	}

	return r, nil
}

func (s *FositeSQLStore) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	return s.r.ClientManager().GetClient(ctx, id)
}

func (s *FositeSQLStore) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	d, err := s.getClientAssertionJWT(ctx, jti)
	if errors.Is(err, sqlcon.ErrNoRows) {
		// the jti is not known => valid
		return nil
	} else if err != nil {
		return err
	}
	if d.Expiry.After(time.Now()) {
		// the jti is not expired yet => invalid
		return errors.WithStack(fosite.ErrJTIKnown)
	}
	// the jti is expired => valid
	return nil
}

func (s *FositeSQLStore) SetClientAssertionJWT(ctx context.Context, j string, exp time.Time) error {
	db := s.db(ctx)

	// delete expired
	if _, err := db.ExecContext(ctx, fmt.Sprintf("DELETE FROM hydra_oauth2_%s WHERE expires_at < now()", sqlTableBlacklistedJTI)); err != nil {
		return sqlcon.HandleError(err)
	}

	if err := s.setClientAssertionJWT(ctx, newBlacklistedJTI(j, exp)); errors.Is(err, sqlcon.ErrUniqueViolation) {
		// found a jti
		return errors.WithStack(fosite.ErrJTIKnown)
	} else if err != nil {
		return err
	}
	// setting worked without a problem
	return nil
}

func (s *FositeSQLStore) getClientAssertionJWT(ctx context.Context, j string) (*BlacklistedJTI, error) {
	sig := signatureFromJTI(j)
	jti := BlacklistedJTI{
		JTI: j,
	}
	db := s.db(ctx)

	return &jti, sqlcon.HandleError(db.GetContext(ctx, &jti, db.Rebind(fmt.Sprintf("SELECT * FROM hydra_oauth2_%s WHERE signature=?", sqlTableBlacklistedJTI)), sig))
}

func (s *FositeSQLStore) setClientAssertionJWT(ctx context.Context, jti *BlacklistedJTI) error {
	db := s.db(ctx)
	_, err := db.ExecContext(ctx, db.Rebind(fmt.Sprintf("INSERT INTO hydra_oauth2_%s (signature, expires_at) VALUES (?, ?)", sqlTableBlacklistedJTI)), jti.Signature, jti.Expiry)

	return sqlcon.HandleError(err)
}

func (s *FositeSQLStore) Authenticate(ctx context.Context, id string, secret []byte) (*client.Client, error) {
	return s.r.ClientManager().Authenticate(ctx, id, secret)
}

func (s *FositeSQLStore) CreateClient(ctx context.Context, c *client.Client) error {
	return s.r.ClientManager().CreateClient(ctx, c)

}

func (s *FositeSQLStore) UpdateClient(ctx context.Context, c *client.Client) error {
	return s.r.ClientManager().UpdateClient(ctx, c)

}

func (s *FositeSQLStore) DeleteClient(ctx context.Context, id string) error {
	return s.r.ClientManager().DeleteClient(ctx, id)

}

func (s *FositeSQLStore) GetClients(ctx context.Context, limit, offset int) (map[string]client.Client, error) {
	interim, err := s.r.ClientManager().GetClients(ctx, limit, offset)
	if err != nil {
		return nil, err
	}

	clients := map[string]client.Client{}
	for _, c := range interim {
		clients[c.GetID()] = c
	}

	return clients, nil
}

func (s *FositeSQLStore) GetConcreteClient(ctx context.Context, id string) (*client.Client, error) {
	return s.r.ClientManager().GetConcreteClient(ctx, id)
}

// hashSignature prevents errors where the signature is longer than 128 characters (and thus doesn't fit into the pk).
func (s *FositeSQLStore) hashSignature(signature string, table tableName) string {
	if table == sqlTableAccess && s.c.IsUsingJWTAsAccessTokens() {
		return fmt.Sprintf("%x", sha512.Sum384([]byte(signature)))
	}
	return signature
}

func (s *FositeSQLStore) createSession(ctx context.Context, signature string, requester fosite.Requester, table tableName) error {
	db := s.db(ctx)
	signature = s.hashSignature(signature, table)

	data, err := sqlSchemaFromRequest(signature, requester, s.c, s.kc, s.r.Logger())
	if err != nil {
		return err
	}

	/* #nosec G201 - sqlParams is a "constant" array */
	query := fmt.Sprintf(
		"INSERT INTO hydra_oauth2_%s (%s) VALUES (%s)",
		table,
		strings.Join(sqlParams, ", "),
		":"+strings.Join(sqlParams, ", :"),
	)
	if _, err := db.NamedExecContext(ctx, query, data); err != nil {
		err = sqlcon.HandleError(err)
		switch errCause := errors.Cause(err).(type) {
		case *herodot.DefaultError:
			if errCause == sqlcon.ErrConcurrentUpdate {
				return errors.Wrap(fosite.ErrSerializationFailure, err.Error())
			}
		}
		return err
	}
	return nil
}

func (s *FositeSQLStore) db(ctx context.Context) sqlxDB {
	if tx, ok := ctx.Value(txKey).(*sqlx.Tx); ok {
		return tx
	} else {
		return s.DB
	}
}

func (s *FositeSQLStore) findSessionBySignature(ctx context.Context, signature string, session fosite.Session, table tableName) (fosite.Requester, error) {
	db := s.db(ctx)
	signature = s.hashSignature(signature, table)

	var d SQLData
	/* #nosec G201 - table is a fixed enum */
	if err := db.GetContext(ctx, &d, db.Rebind(fmt.Sprintf("SELECT * FROM hydra_oauth2_%s WHERE signature=?", table)), signature); err == sql.ErrNoRows {
		return nil, errors.Wrap(fosite.ErrNotFound, "")
	} else if err != nil {
		return nil, sqlcon.HandleError(err)
	} else if !d.Active && table == sqlTableCode {
		if r, err := d.toRequest(session, s.r.ClientManager(), s.c, s.kc, s.r.Logger()); err != nil {
			return nil, err
		} else {
			return r, errors.WithStack(fosite.ErrInvalidatedAuthorizeCode)
		}
	} else if !d.Active {
		return nil, errors.WithStack(fosite.ErrInactiveToken)
	}

	return d.toRequest(session, s.r.ClientManager(), s.c, s.kc, s.r.Logger())
}

func (s *FositeSQLStore) deleteSession(ctx context.Context, signature string, table tableName) error {
	db := s.db(ctx)
	signature = s.hashSignature(signature, table)

	/* #nosec G201 - table is a const */
	if _, err := db.ExecContext(ctx, s.DB.Rebind(fmt.Sprintf("DELETE FROM hydra_oauth2_%s WHERE signature=?", table)), signature); err != nil {
		return sqlcon.HandleError(err)
	}
	return nil
}

func (s *FositeSQLStore) CreateOpenIDConnectSession(ctx context.Context, signature string, requester fosite.Requester) error {
	return s.createSession(ctx, signature, requester, sqlTableOpenID)
}

func (s *FositeSQLStore) GetOpenIDConnectSession(ctx context.Context, signature string, requester fosite.Requester) (fosite.Requester, error) {
	return s.findSessionBySignature(ctx, signature, requester.GetSession(), sqlTableOpenID)
}

func (s *FositeSQLStore) DeleteOpenIDConnectSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, signature, sqlTableOpenID)
}

func (s *FositeSQLStore) CreateAuthorizeCodeSession(ctx context.Context, signature string, requester fosite.Requester) error {
	return s.createSession(ctx, signature, requester, sqlTableCode)
}

func (s *FositeSQLStore) GetAuthorizeCodeSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.findSessionBySignature(ctx, signature, session, sqlTableCode)
}

func (s *FositeSQLStore) InvalidateAuthorizeCodeSession(ctx context.Context, signature string) error {
	db := s.db(ctx)
	if _, err := db.ExecContext(ctx, db.Rebind(fmt.Sprintf(
		"UPDATE hydra_oauth2_%s SET active=false WHERE signature=?",
		sqlTableCode,
	)), signature); err != nil {
		return sqlcon.HandleError(err)
	}

	return nil
}

func (s *FositeSQLStore) CreateAccessTokenSession(ctx context.Context, signature string, requester fosite.Requester) error {
	return s.createSession(ctx, signature, requester, sqlTableAccess)
}

func (s *FositeSQLStore) GetAccessTokenSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.findSessionBySignature(ctx, signature, session, sqlTableAccess)
}

func (s *FositeSQLStore) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, signature, sqlTableAccess)
}

func (s *FositeSQLStore) CreateRefreshTokenSession(ctx context.Context, signature string, requester fosite.Requester) error {
	return s.createSession(ctx, signature, requester, sqlTableRefresh)
}

func (s *FositeSQLStore) GetRefreshTokenSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.findSessionBySignature(ctx, signature, session, sqlTableRefresh)
}

func (s *FositeSQLStore) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, signature, sqlTableRefresh)
}

func (s *FositeSQLStore) CreatePKCERequestSession(ctx context.Context, signature string, requester fosite.Requester) error {
	return s.createSession(ctx, signature, requester, sqlTablePKCE)
}

func (s *FositeSQLStore) GetPKCERequestSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.findSessionBySignature(ctx, signature, session, sqlTablePKCE)
}

func (s *FositeSQLStore) DeletePKCERequestSession(ctx context.Context, signature string) error {
	return s.deleteSession(ctx, signature, sqlTablePKCE)
}

func (s *FositeSQLStore) CreateImplicitAccessTokenSession(ctx context.Context, signature string, requester fosite.Requester) error {
	return s.CreateAccessTokenSession(ctx, signature, requester)
}

func (s *FositeSQLStore) RevokeRefreshToken(ctx context.Context, id string) error {
	return s.revokeSession(ctx, id, sqlTableRefresh)
}

func (s *FositeSQLStore) RevokeAccessToken(ctx context.Context, id string) error {
	return s.revokeSession(ctx, id, sqlTableAccess)
}

func (s *FositeSQLStore) revokeSession(ctx context.Context, id string, table tableName) error {
	db := s.db(ctx)
	/* #nosec G201 - table is a const enum */
	if _, err := db.ExecContext(ctx, db.Rebind(fmt.Sprintf("DELETE FROM hydra_oauth2_%s WHERE request_id=?", table)), id); err == sql.ErrNoRows {
		return errors.Wrap(fosite.ErrNotFound, "")
	} else if err := sqlcon.HandleError(err); err != nil {
		switch errCause := errors.Cause(err).(type) {
		case *herodot.DefaultError:
			if errCause == sqlcon.ErrConcurrentUpdate {
				return errors.Wrap(fosite.ErrSerializationFailure, err.Error())
			}
		}
		return err
	}
	return nil
}

func (s *FositeSQLStore) FlushInactiveAccessTokens(ctx context.Context, notAfter time.Time) error {
	if _, err := s.DB.ExecContext(ctx, s.DB.Rebind(fmt.Sprintf("DELETE FROM hydra_oauth2_%s WHERE requested_at < ? AND requested_at < ?", sqlTableAccess)), time.Now().Add(-s.c.AccessTokenLifespan()), notAfter); err == sql.ErrNoRows {
		return errors.Wrap(fosite.ErrNotFound, "")
	} else if err != nil {
		return sqlcon.HandleError(err)
	}

	return nil
}

func (s *FositeSQLStore) BeginTX(ctx context.Context) (context.Context, error) {
	if tx, err := s.DB.BeginTxx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  false,
	}); err != nil {
		return ctx, err
	} else {
		return context.WithValue(ctx, txKey, tx), nil
	}
}

func (s *FositeSQLStore) Commit(ctx context.Context) error {
	if tx, ok := ctx.Value(txKey).(*sqlx.Tx); !ok {
		return errors.Wrap(fosite.ErrServerError, "commit failed: no transaction stored in context")
	} else {
		if err := sqlcon.HandleError(tx.Commit()); err != nil {
			switch errCause := errors.Cause(err).(type) {
			case *herodot.DefaultError:
				if errCause == sqlcon.ErrConcurrentUpdate {
					return errors.Wrap(fosite.ErrSerializationFailure, err.Error())
				}
			}
			return err
		}
		return nil
	}
}

func (s *FositeSQLStore) Rollback(ctx context.Context) error {
	if tx, ok := ctx.Value(txKey).(*sqlx.Tx); !ok {
		return errors.Wrap(fosite.ErrServerError, "rollback failed: no transaction stored in context")
	} else {
		return tx.Rollback()
	}
}

func (s *FositeSQLStore) DeleteAccessTokens(ctx context.Context, clientID string) error {
	if _, err := s.DB.ExecContext(ctx, s.DB.Rebind(fmt.Sprintf("DELETE FROM hydra_oauth2_%s WHERE client_id=?", sqlTableAccess)), clientID); err == sql.ErrNoRows {
		return errors.Wrap(fosite.ErrNotFound, "")
	} else if err != nil {
		return sqlcon.HandleError(err)
	}

	return nil
}