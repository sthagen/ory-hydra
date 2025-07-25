// Copyright © 2022 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ory/fosite"
	"github.com/ory/hydra/v2/client"
	"github.com/ory/hydra/v2/consent"
	"github.com/ory/hydra/v2/flow"
	"github.com/ory/hydra/v2/oauth2/flowctx"
	"github.com/ory/hydra/v2/x"
	"github.com/ory/pop/v6"
	"github.com/ory/x/errorsx"
	"github.com/ory/x/otelx"
	keysetpagination "github.com/ory/x/pagination/keysetpagination_v2"
	"github.com/ory/x/popx"
	"github.com/ory/x/sqlcon"
	"github.com/ory/x/sqlxx"
)

var _ consent.Manager = &Persister{}

func (p *Persister) RevokeSubjectConsentSession(ctx context.Context, user string) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.RevokeSubjectConsentSession")
	defer otelx.End(span, &err)

	return p.Transaction(ctx, p.revokeConsentSession("consent_challenge_id IS NOT NULL AND subject = ?", user))
}

func (p *Persister) RevokeSubjectClientConsentSession(ctx context.Context, user, client string) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.RevokeSubjectClientConsentSession", trace.WithAttributes(attribute.String("client", client)))
	defer otelx.End(span, &err)

	return p.Transaction(ctx, p.revokeConsentSession("consent_challenge_id IS NOT NULL AND subject = ? AND client_id = ?", user, client))
}

func (p *Persister) RevokeConsentSessionByID(ctx context.Context, consentRequestID string) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.RevokeConsentSessionByID",
		trace.WithAttributes(attribute.String("consent_challenge_id", consentRequestID)))
	defer otelx.End(span, &err)

	return p.Transaction(ctx, p.revokeConsentSession("consent_challenge_id = ?", consentRequestID))
}

func (p *Persister) revokeConsentSession(whereStmt string, whereArgs ...interface{}) func(context.Context, *pop.Connection) error {
	return func(ctx context.Context, c *pop.Connection) error {
		fs := make([]*flow.Flow, 0)
		if err := p.QueryWithNetwork(ctx).
			Where(whereStmt, whereArgs...).
			Select("consent_challenge_id").
			All(&fs); errors.Is(err, sql.ErrNoRows) {
			return errorsx.WithStack(x.ErrNotFound)
		} else if err != nil {
			return sqlcon.HandleError(err)
		}

		ids := make([]interface{}, 0, len(fs))
		nid := p.NetworkID(ctx)
		for _, f := range fs {
			ids = append(ids, f.ConsentRequestID.String())
		}

		if len(ids) == 0 {
			return nil
		}

		if err := p.QueryWithNetwork(ctx).
			Where("nid = ?", nid).
			Where("request_id IN (?)", ids...).
			Delete(OAuth2RequestSQL{Table: sqlTableAccess}.TableName()); errors.Is(err, fosite.ErrNotFound) {
			// do nothing
		} else if err != nil {
			return err
		}

		if err := p.QueryWithNetwork(ctx).
			Where("nid = ?", nid).
			Where("request_id IN (?)", ids...).
			Delete(OAuth2RefreshTable{}.TableName()); errors.Is(err, fosite.ErrNotFound) {
			// do nothing
		} else if err != nil {
			return err
		}

		if err := p.QueryWithNetwork(ctx).
			Where("nid = ?", nid).
			Where("consent_challenge_id IN (?)", ids...).
			Delete(new(flow.Flow)); errors.Is(err, sql.ErrNoRows) {
			return errorsx.WithStack(x.ErrNotFound)
		} else if err != nil {
			return sqlcon.HandleError(err)
		}

		return nil
	}
}

func (p *Persister) RevokeSubjectLoginSession(ctx context.Context, subject string) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.RevokeSubjectLoginSession")
	defer otelx.End(span, &err)

	err = p.QueryWithNetwork(ctx).Where("subject = ?", subject).Delete(&flow.LoginSession{})
	if err != nil {
		return sqlcon.HandleError(err)
	}

	// This confuses people, see https://github.com/ory/hydra/issues/1168
	//
	// count, _ := rows.RowsAffected()
	// if count == 0 {
	// 	 return errorsx.WithStack(x.ErrNotFound)
	// }

	return nil
}

func (p *Persister) CreateForcedObfuscatedLoginSession(ctx context.Context, session *consent.ForcedObfuscatedLoginSession) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.CreateForcedObfuscatedLoginSession")
	defer otelx.End(span, &err)

	return p.Transaction(ctx, func(ctx context.Context, c *pop.Connection) error {
		nid := p.NetworkID(ctx)
		if err := c.RawQuery(
			"DELETE FROM hydra_oauth2_obfuscated_authentication_session WHERE nid = ? AND client_id = ? AND subject = ?",
			nid,
			session.ClientID,
			session.Subject,
		).Exec(); err != nil {
			return sqlcon.HandleError(err)
		}

		return sqlcon.HandleError(c.RawQuery(
			"INSERT INTO hydra_oauth2_obfuscated_authentication_session (nid, subject, client_id, subject_obfuscated) VALUES (?, ?, ?, ?)",
			nid,
			session.Subject,
			session.ClientID,
			session.SubjectObfuscated,
		).Exec())
	})
}

func (p *Persister) GetForcedObfuscatedLoginSession(ctx context.Context, client, obfuscated string) (_ *consent.ForcedObfuscatedLoginSession, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.GetForcedObfuscatedLoginSession", trace.WithAttributes(attribute.String("client", client)))
	defer otelx.End(span, &err)

	var s consent.ForcedObfuscatedLoginSession

	if err := p.Connection(ctx).Where(
		"client_id = ? AND subject_obfuscated = ? AND nid = ?",
		client,
		obfuscated,
		p.NetworkID(ctx),
	).First(&s); errors.Is(err, sql.ErrNoRows) {
		return nil, errorsx.WithStack(x.ErrNotFound)
	} else if err != nil {
		return nil, sqlcon.HandleError(err)
	}

	return &s, nil
}

// CreateConsentRequest configures fields that are introduced or changed in the
// consent request. It doesn't touch fields that would be copied from the login
// request.
func (p *Persister) CreateConsentRequest(ctx context.Context, f *flow.Flow, req *flow.OAuth2ConsentRequest) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.CreateConsentRequest")
	defer otelx.End(span, &err)

	if f == nil {
		return errorsx.WithStack(x.ErrNotFound.WithDebug("Flow is nil"))
	}
	if f.ID != req.LoginChallenge.String() || f.NID != p.NetworkID(ctx) {
		return errorsx.WithStack(x.ErrNotFound)
	}
	f.State = flow.FlowStateConsentInitialized
	f.ConsentRequestID = sqlxx.NullString(req.ConsentRequestID)
	f.ConsentSkip = req.Skip
	f.ConsentVerifier = sqlxx.NullString(req.Verifier)
	f.ConsentCSRF = sqlxx.NullString(req.CSRF)

	return nil
}

func (p *Persister) GetFlowByConsentChallenge(ctx context.Context, challenge string) (_ *flow.Flow, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.GetFlowByConsentChallenge")
	defer otelx.End(span, &err)

	f, err := flowctx.Decode[flow.Flow](ctx, p.r.FlowCipher(), challenge, flowctx.AsConsentChallenge)
	if err != nil {
		return nil, errorsx.WithStack(x.ErrNotFound.WithWrap(err))
	}
	if f.NID != p.NetworkID(ctx) {
		return nil, errorsx.WithStack(x.ErrNotFound.WithDescription("Network IDs are not matching."))
	}
	if f.RequestedAt.Add(p.config.ConsentRequestMaxAge(ctx)).Before(time.Now()) {
		return nil, errorsx.WithStack(fosite.ErrRequestUnauthorized.WithHint("The consent request has expired, please try again."))
	}

	return f, nil
}

func (p *Persister) GetConsentRequest(ctx context.Context, challenge string) (_ *flow.OAuth2ConsentRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.GetConsentRequest")
	defer otelx.End(span, &err)

	f, err := p.GetFlowByConsentChallenge(ctx, challenge)
	if err != nil {
		if errors.Is(err, sqlcon.ErrNoRows) {
			return nil, errorsx.WithStack(x.ErrNotFound)
		}
		return nil, err
	}

	return f.GetConsentRequest(challenge), nil
}

// CreateDeviceUserAuthRequest creates a new flow from a DeviceUserAuthRequest.
func (p *Persister) CreateDeviceUserAuthRequest(ctx context.Context, req *flow.DeviceUserAuthRequest) (_ *flow.Flow, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.CreateDeviceUserAuthRequest")
	defer otelx.End(span, &err)

	nid := p.NetworkID(ctx)
	f := flow.NewDeviceFlow(req)
	f.NID = nid

	return f, nil
}

// GetDeviceUserAuthRequest decodes a challenge into a new DeviceUserAuthRequest.
func (p *Persister) GetDeviceUserAuthRequest(ctx context.Context, challenge string) (_ *flow.DeviceUserAuthRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.GetDeviceUserAuthRequest")
	defer otelx.End(span, &err)

	f, err := flowctx.Decode[flow.Flow](ctx, p.r.FlowCipher(), challenge, flowctx.AsDeviceChallenge)
	if err != nil {
		return nil, errorsx.WithStack(x.ErrNotFound.WithWrap(err))
	}
	if f.NID != p.NetworkID(ctx) {
		return nil, errorsx.WithStack(x.ErrNotFound)
	}
	if f.RequestedAt.Add(p.config.ConsentRequestMaxAge(ctx)).Before(time.Now()) {
		return nil, errorsx.WithStack(fosite.ErrRequestUnauthorized.WithHint("The device request has expired, please try again."))
	}

	return f.GetDeviceUserAuthRequest(), nil
}

// HandleDeviceUserAuthRequest uses a HandledDeviceUserAuthRequest to update the flow and returns a DeviceUserAuthRequest.
func (p *Persister) HandleDeviceUserAuthRequest(ctx context.Context, f *flow.Flow, challenge string, r *flow.HandledDeviceUserAuthRequest) (_ *flow.DeviceUserAuthRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.HandleDeviceUserAuthRequest")
	defer otelx.End(span, &err)

	if f == nil {
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithDebug("Flow was nil"))
	}
	if f.NID != p.NetworkID(ctx) {
		return nil, errorsx.WithStack(x.ErrNotFound)
	}
	err = f.HandleDeviceUserAuthRequest(r)
	if err != nil {
		return nil, err
	}

	return p.GetDeviceUserAuthRequest(ctx, challenge)
}

// VerifyAndInvalidateDeviceUserAuthRequest verifies a verifier and invalidates the flow.
func (p *Persister) VerifyAndInvalidateDeviceUserAuthRequest(ctx context.Context, verifier string) (_ *flow.HandledDeviceUserAuthRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.VerifyAndInvalidateDeviceUserAuthRequest")
	defer otelx.End(span, &err)

	f, err := flowctx.Decode[flow.Flow](ctx, p.r.FlowCipher(), verifier, flowctx.AsDeviceVerifier)
	if err != nil {
		return nil, errorsx.WithStack(fosite.ErrAccessDenied.WithHint("The device verifier has already been used, has not been granted, or is invalid."))
	}
	if f.NID != p.NetworkID(ctx) {
		return nil, errorsx.WithStack(sqlcon.ErrNoRows)
	}

	if err = f.InvalidateDeviceRequest(); err != nil {
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithDebug(err.Error()))
	}

	return f.GetHandledDeviceUserAuthRequest(), nil
}

func (p *Persister) CreateLoginRequestFromDeviceRequest(ctx context.Context, f *flow.Flow, req *flow.LoginRequest) (_ *flow.Flow, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.CreateLoginRequestFromDeviceRequest")
	defer otelx.End(span, &err)

	f.ID = req.ID
	f.LoginSkip = req.Skip
	f.Subject = req.Subject
	f.SessionID = req.SessionID
	f.LoginWasUsed = req.WasHandled
	f.ForceSubjectIdentifier = req.ForceSubjectIdentifier
	f.LoginVerifier = req.Verifier
	f.LoginCSRF = req.CSRF
	f.LoginAuthenticatedAt = req.AuthenticatedAt
	f.RequestedAt = req.RequestedAt
	f.State = flow.FlowStateLoginInitialized

	nid := p.NetworkID(ctx)
	if nid == uuid.Nil {
		return nil, errorsx.WithStack(x.ErrNotFound)
	}
	f.NID = nid

	return f, nil
}

func (p *Persister) CreateLoginRequest(ctx context.Context, req *flow.LoginRequest) (_ *flow.Flow, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.CreateLoginRequest")
	defer otelx.End(span, &err)

	f := flow.NewFlow(req)

	nid := p.NetworkID(ctx)
	if nid == uuid.Nil {
		return nil, errorsx.WithStack(x.ErrNotFound)
	}
	f.NID = nid

	return f, nil
}

func (p *Persister) GetFlow(ctx context.Context, loginChallenge string) (_ *flow.Flow, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.GetFlow")
	defer otelx.End(span, &err)

	var f flow.Flow
	if err := p.QueryWithNetwork(ctx).Where("login_challenge = ?", loginChallenge).First(&f); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errorsx.WithStack(x.ErrNotFound)
		}
		return nil, sqlcon.HandleError(err)
	}
	return &f, nil
}

func (p *Persister) GetLoginRequest(ctx context.Context, loginChallenge string) (_ *flow.LoginRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.GetLoginRequest")
	defer otelx.End(span, &err)

	f, err := flowctx.Decode[flow.Flow](ctx, p.r.FlowCipher(), loginChallenge, flowctx.AsLoginChallenge)
	if err != nil {
		return nil, errorsx.WithStack(x.ErrNotFound.WithWrap(err))
	}
	if f.NID != p.NetworkID(ctx) {
		return nil, errorsx.WithStack(x.ErrNotFound)
	}
	if f.RequestedAt.Add(p.config.ConsentRequestMaxAge(ctx)).Before(time.Now()) {
		return nil, errorsx.WithStack(fosite.ErrRequestUnauthorized.WithHint("The login request has expired, please try again."))
	}
	lr := f.GetLoginRequest()
	// Restore the short challenge ID, which was previously sent to the encoded flow,
	// to make sure that the challenge ID in the returned flow matches the param.
	lr.ID = loginChallenge

	return lr, nil
}

func (p *Persister) HandleConsentRequest(ctx context.Context, f *flow.Flow, r *flow.AcceptOAuth2ConsentRequest) (_ *flow.OAuth2ConsentRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.HandleConsentRequest")
	defer otelx.End(span, &err)

	if f == nil {
		return nil, errors.WithStack(fosite.ErrInvalidRequest.WithDebug("Flow was nil"))
	}
	if f.NID != p.NetworkID(ctx) {
		return nil, errors.WithStack(x.ErrNotFound)
	}
	if err := f.HandleConsentRequest(r); err != nil {
		return nil, err
	}

	return f.GetConsentRequest( /* No longer available and no longer needed: challenge =  */ ""), nil
}

func (p *Persister) VerifyAndInvalidateConsentRequest(ctx context.Context, verifier string) (_ *flow.AcceptOAuth2ConsentRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.VerifyAndInvalidateConsentRequest")
	defer otelx.End(span, &err)

	f, err := flowctx.Decode[flow.Flow](ctx, p.r.FlowCipher(), verifier, flowctx.AsConsentVerifier)
	if err != nil {
		return nil, errorsx.WithStack(fosite.ErrAccessDenied.WithHint("The consent verifier has already been used, has not been granted, or is invalid."))
	}
	if f.NID != p.NetworkID(ctx) {
		return nil, errorsx.WithStack(sqlcon.ErrNoRows)
	}

	if err = f.InvalidateConsentRequest(); err != nil {
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithDebug(err.Error()))
	}

	if err = p.Connection(ctx).Create(f); err != nil {
		return nil, sqlcon.HandleError(err)
	}

	return f.GetHandledConsentRequest(), nil
}

func (p *Persister) HandleLoginRequest(ctx context.Context, f *flow.Flow, challenge string, r *flow.HandledLoginRequest) (lr *flow.LoginRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.HandleLoginRequest")
	defer otelx.End(span, &err)

	if f == nil {
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithDebug("Flow was nil"))
	}
	if f.NID != p.NetworkID(ctx) {
		return nil, errorsx.WithStack(x.ErrNotFound)
	}
	r.ID = f.ID
	err = f.HandleLoginRequest(r)
	if err != nil {
		return nil, err
	}

	return p.GetLoginRequest(ctx, challenge)
}

func (p *Persister) VerifyAndInvalidateLoginRequest(ctx context.Context, verifier string) (_ *flow.HandledLoginRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.VerifyAndInvalidateLoginRequest")
	defer otelx.End(span, &err)

	f, err := flowctx.Decode[flow.Flow](ctx, p.r.FlowCipher(), verifier, flowctx.AsLoginVerifier)
	if err != nil {
		return nil, errorsx.WithStack(sqlcon.ErrNoRows)
	}
	if f.NID != p.NetworkID(ctx) {
		return nil, errorsx.WithStack(sqlcon.ErrNoRows)
	}

	if err := f.InvalidateLoginRequest(); err != nil {
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithDebug(err.Error()))
	}
	d := f.GetHandledLoginRequest()

	return &d, nil
}

func (p *Persister) GetRememberedLoginSession(ctx context.Context, loginSessionFromCookie *flow.LoginSession, id string) (_ *flow.LoginSession, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.GetRememberedLoginSession")
	defer otelx.End(span, &err)

	if s := loginSessionFromCookie; s != nil && s.NID == p.NetworkID(ctx) && s.ID == id && s.Remember {
		return s, nil
	}

	var s flow.LoginSession

	if err := p.QueryWithNetwork(ctx).Where("remember = TRUE").Find(&s, id); errors.Is(err, sql.ErrNoRows) {
		return nil, errorsx.WithStack(x.ErrNotFound)
	} else if err != nil {
		return nil, sqlcon.HandleError(err)
	}

	return &s, nil
}

// ConfirmLoginSession creates or updates the login session. The NID will be set to the network ID of the context.
func (p *Persister) ConfirmLoginSession(ctx context.Context, loginSession *flow.LoginSession) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.ConfirmLoginSession")
	defer otelx.End(span, &err)

	loginSession.NID = p.NetworkID(ctx)
	loginSession.AuthenticatedAt = sqlxx.NullTime(time.Time(loginSession.AuthenticatedAt).Truncate(time.Second))
	loginSession.ExpiresAt = sqlxx.NullTime(time.Now().Truncate(time.Second).Add(p.config.GetAuthenticationSessionLifespan(ctx)))

	if p.Connection(ctx).Dialect.Name() == "mysql" {
		// MySQL does not support UPSERT.
		return p.mySQLConfirmLoginSession(ctx, loginSession)
	}

	res, err := p.Connection(ctx).Store.NamedExecContext(ctx, `
INSERT INTO hydra_oauth2_authentication_session (id, nid, authenticated_at, subject, remember, identity_provider_session_id, expires_at)
VALUES (:id, :nid, :authenticated_at, :subject, :remember, :identity_provider_session_id, :expires_at)
ON CONFLICT(id) DO
UPDATE SET
	authenticated_at = :authenticated_at,
	subject = :subject,
	remember = :remember,
	identity_provider_session_id = :identity_provider_session_id,
	expires_at = :expires_at
WHERE hydra_oauth2_authentication_session.id = :id AND hydra_oauth2_authentication_session.nid = :nid
`, loginSession)
	if err != nil {
		return sqlcon.HandleError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return sqlcon.HandleError(err)
	}
	if n == 0 {
		return errors.WithStack(x.ErrNotFound)
	}
	return nil
}

func (p *Persister) CreateLoginSession(ctx context.Context, session *flow.LoginSession) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.CreateLoginSession")
	defer otelx.End(span, &err)

	nid := p.NetworkID(ctx)
	if nid == uuid.Nil {
		return errorsx.WithStack(x.ErrNotFound)
	}
	session.NID = nid

	return nil
}

func (p *Persister) DeleteLoginSession(ctx context.Context, id string) (_ *flow.LoginSession, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.DeleteLoginSession")
	defer otelx.End(span, &err)

	c := p.Connection(ctx)
	if c.Dialect.Name() == "mysql" {
		// MySQL does not support RETURNING.
		return p.mySQLDeleteLoginSession(ctx, id)
	}

	var session flow.LoginSession
	columns := pop.NewModel(&session, ctx).Columns().Readable()
	if err := p.Connection(ctx).RawQuery(
		fmt.Sprintf(
			`DELETE FROM hydra_oauth2_authentication_session WHERE id = ? AND nid = ? RETURNING %s`,
			columns.QuotedString(c.Dialect)),
		id,
		p.NetworkID(ctx),
	).First(&session); err != nil {
		return nil, sqlcon.HandleError(err)
	}

	return &session, nil
}

func (p *Persister) mySQLDeleteLoginSession(ctx context.Context, id string) (_ *flow.LoginSession, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.mySQLDeleteLoginSession")
	defer otelx.End(span, &err)

	var session flow.LoginSession
	if err := p.Connection(ctx).Transaction(func(tx *pop.Connection) error {
		if err := tx.Where("id = ? AND nid = ?", id, p.NetworkID(ctx)).First(&session); err != nil {
			return err
		}

		return tx.RawQuery(
			`DELETE FROM hydra_oauth2_authentication_session WHERE id = ? AND nid = ?`,
			id, p.NetworkID(ctx),
		).Exec()
	}); err != nil {
		return nil, sqlcon.HandleError(err)
	}

	return &session, nil

}

func (p *Persister) FindGrantedAndRememberedConsentRequest(ctx context.Context, client, subject string) (_ *flow.Flow, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.FindGrantedAndRememberedConsentRequest")
	defer otelx.End(span, &err)

	var f flow.Flow
	err = p.QueryWithNetwork(ctx).
		Where("state = ?", flow.FlowStateConsentUsed).
		Where("subject = ?", subject).
		Where("client_id = ?", client).
		Where("consent_skip = FALSE").
		Where("consent_error = '{}'").
		Where("consent_remember = TRUE").
		Order("requested_at DESC").
		First(&f)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.WithStack(consent.ErrNoPreviousConsentFound)
	} else if err != nil {
		return nil, sqlcon.HandleError(err)
	}

	fs := filterExpiredConsentRequests([]flow.Flow{f})
	if len(fs) == 0 {
		return nil, errors.WithStack(consent.ErrNoPreviousConsentFound)
	}
	return &fs[0], nil
}

func (p *Persister) FindSubjectsGrantedConsentRequests(ctx context.Context, subject string, pageOpts ...keysetpagination.Option) (_ []flow.AcceptOAuth2ConsentRequest, _ *keysetpagination.Paginator, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.FindSubjectsGrantedConsentRequests")
	defer otelx.End(span, &err)

	paginator := keysetpagination.NewPaginator(append(pageOpts,
		keysetpagination.WithDefaultToken(keysetpagination.NewPageToken(keysetpagination.Column{Name: "login_challenge", Value: ""})),
	)...)

	var fs []flow.Flow
	err = p.QueryWithNetwork(ctx).
		Where("state IN (?, ?)", flow.FlowStateConsentUsed, flow.FlowStateConsentUnused).
		Where("subject = ?", subject).
		Where("consent_skip = FALSE").
		Where("consent_error = '{}'").
		Scope(keysetpagination.Paginate[flow.Flow](paginator)).
		All(&fs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, errors.WithStack(consent.ErrNoPreviousConsentFound)
	} else if err != nil {
		return nil, nil, sqlcon.HandleError(err)
	}

	fs = filterExpiredConsentRequests(fs)
	if len(fs) == 0 {
		return nil, nil, errors.WithStack(consent.ErrNoPreviousConsentFound)
	}
	fs, nextPage := keysetpagination.Result(fs, paginator)

	rs := make([]flow.AcceptOAuth2ConsentRequest, len(fs))
	for i := range fs {
		rs[i] = *(fs[i].GetHandledConsentRequest())
	}

	return rs, nextPage, nil
}

func (p *Persister) FindSubjectsSessionGrantedConsentRequests(ctx context.Context, subject, sid string, pageOpts ...keysetpagination.Option) (_ []flow.AcceptOAuth2ConsentRequest, _ *keysetpagination.Paginator, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.FindSubjectsSessionGrantedConsentRequests", trace.WithAttributes(attribute.String("sid", sid)))
	defer otelx.End(span, &err)

	paginator := keysetpagination.NewPaginator(append(pageOpts,
		keysetpagination.WithDefaultToken(keysetpagination.NewPageToken(keysetpagination.Column{Name: "login_challenge", Value: ""})),
	)...)

	var fs []flow.Flow
	err = p.QueryWithNetwork(ctx).
		Where("state IN (?, ?)", flow.FlowStateConsentUsed, flow.FlowStateConsentUnused).
		Where("subject = ?", subject).
		Where("login_session_id = ?", sid).
		Where("consent_skip = FALSE").
		Where("consent_error = '{}'").
		Scope(keysetpagination.Paginate[flow.Flow](paginator)).
		All(&fs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, errors.WithStack(consent.ErrNoPreviousConsentFound)
	} else if err != nil {
		return nil, nil, sqlcon.HandleError(err)
	}

	fs = filterExpiredConsentRequests(fs)
	if len(fs) == 0 {
		return nil, nil, errors.WithStack(consent.ErrNoPreviousConsentFound)
	}

	fs, nextPage := keysetpagination.Result(fs, paginator)

	rs := make([]flow.AcceptOAuth2ConsentRequest, len(fs))
	for i := range fs {
		rs[i] = *(fs[i].GetHandledConsentRequest())
	}

	return rs, nextPage, nil
}

func (p *Persister) CountSubjectsGrantedConsentRequests(ctx context.Context, subject string) (n int, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.CountSubjectsGrantedConsentRequests")
	defer otelx.End(span, &err)
	defer func() {
		span.SetAttributes(attribute.Int("count", n))
	}()

	n, err = p.Connection(ctx).
		Where(
			strings.TrimSpace(fmt.Sprintf(`
(state = %d OR state = %d) AND
subject = ? AND
consent_skip=FALSE AND
consent_error='{}' AND
nid = ?`, flow.FlowStateConsentUsed, flow.FlowStateConsentUnused,
			)),
			subject, p.NetworkID(ctx)).
		Count(&flow.Flow{})
	return n, sqlcon.HandleError(err)
}

func filterExpiredConsentRequests(requests []flow.Flow) []flow.Flow {
	result := make([]flow.Flow, 0, len(requests))
	for _, v := range requests {
		rememberFor := 0
		if v.ConsentRememberFor != nil {
			rememberFor = *v.ConsentRememberFor
		}
		if rememberFor > 0 && v.RequestedAt.Add(time.Duration(rememberFor)*time.Second).Before(time.Now().UTC()) {
			continue
		}
		result = append(result, v)
	}
	return result
}

func (p *Persister) ListUserAuthenticatedClientsWithFrontChannelLogout(ctx context.Context, subject, sid string) (_ []client.Client, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.ListUserAuthenticatedClientsWithFrontChannelLogout")
	defer otelx.End(span, &err)

	return p.listUserAuthenticatedClients(ctx, subject, sid, "front")
}

func (p *Persister) ListUserAuthenticatedClientsWithBackChannelLogout(ctx context.Context, subject, sid string) (_ []client.Client, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.ListUserAuthenticatedClientsWithBackChannelLogout")
	defer otelx.End(span, &err)

	return p.listUserAuthenticatedClients(ctx, subject, sid, "back")
}

func (p *Persister) listUserAuthenticatedClients(ctx context.Context, subject, sid, channel string) (cs []client.Client, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.listUserAuthenticatedClients",
		trace.WithAttributes(attribute.String("sid", sid)))
	defer otelx.End(span, &err)

	conn := p.Connection(ctx)
	columns := popx.DBColumns[client.Client](&popx.PrefixQuoter{Prefix: "c.", Quoter: conn.Dialect})
	if conn.Dialect.Name() == "mysql" {
		columns = "c.*" // MySQL does not support this.
	}

	if err := conn.RawQuery(
		/* #nosec G201 - channel can either be "front" or "back" */
		fmt.Sprintf(`
SELECT DISTINCT %s FROM hydra_client as c
JOIN hydra_oauth2_flow as f ON (c.id = f.client_id AND c.nid = f.nid)
WHERE
	f.subject=? AND
	c.%schannel_logout_uri != '' AND
	c.%schannel_logout_uri IS NOT NULL AND
	f.login_session_id = ? AND
	f.nid = ? AND
	c.nid = ?`,
			columns,
			channel,
			channel,
		),
		subject,
		sid,
		p.NetworkID(ctx),
		p.NetworkID(ctx),
	).All(&cs); err != nil {
		return nil, sqlcon.HandleError(err)
	}

	return cs, nil
}

func (p *Persister) CreateLogoutRequest(ctx context.Context, request *flow.LogoutRequest) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.CreateLogoutRequest")
	defer otelx.End(span, &err)

	return errorsx.WithStack(p.CreateWithNetwork(ctx, request))
}

func (p *Persister) AcceptLogoutRequest(ctx context.Context, challenge string) (_ *flow.LogoutRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.AcceptLogoutRequest")
	defer otelx.End(span, &err)

	if err := p.Connection(ctx).RawQuery("UPDATE hydra_oauth2_logout_request SET accepted=true, rejected=false WHERE challenge=? AND nid = ?", challenge, p.NetworkID(ctx)).Exec(); err != nil {
		return nil, sqlcon.HandleError(err)
	}

	return p.GetLogoutRequest(ctx, challenge)
}

func (p *Persister) RejectLogoutRequest(ctx context.Context, challenge string) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.RejectLogoutRequest")
	defer otelx.End(span, &err)

	count, err := p.Connection(ctx).
		RawQuery("UPDATE hydra_oauth2_logout_request SET rejected=true, accepted=false WHERE challenge=? AND nid = ?", challenge, p.NetworkID(ctx)).
		ExecWithCount()
	if count == 0 {
		return errorsx.WithStack(x.ErrNotFound)
	} else {
		return errorsx.WithStack(err)
	}
}

func (p *Persister) GetLogoutRequest(ctx context.Context, challenge string) (_ *flow.LogoutRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.GetLogoutRequest")
	defer otelx.End(span, &err)

	var lr flow.LogoutRequest
	return &lr, sqlcon.HandleError(p.QueryWithNetwork(ctx).Where("challenge = ? AND rejected = FALSE", challenge).First(&lr))
}

func (p *Persister) VerifyAndInvalidateLogoutRequest(ctx context.Context, verifier string) (_ *flow.LogoutRequest, err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.VerifyAndInvalidateLogoutRequest")
	defer otelx.End(span, &err)

	var lr flow.LogoutRequest
	if count, err := p.Connection(ctx).RawQuery(`
UPDATE hydra_oauth2_logout_request
  SET was_used = TRUE
WHERE nid = ?
  AND verifier = ?
  AND accepted = TRUE
  AND rejected = FALSE`,
		p.NetworkID(ctx),
		verifier,
	).ExecWithCount(); count == 0 && err == nil {
		return nil, errorsx.WithStack(x.ErrNotFound)
	} else if err != nil {
		return nil, sqlcon.HandleError(err)
	}

	err = sqlcon.HandleError(p.QueryWithNetwork(ctx).Where("verifier = ?", verifier).First(&lr))
	if err != nil {
		return nil, err
	}

	if expiry := time.Time(lr.ExpiresAt);
	// If the expiry is unset, we are in a legacy use case (allow logout).
	// TODO: Remove this in the future.
	!expiry.IsZero() && expiry.Before(time.Now().UTC()) {
		return nil, errorsx.WithStack(flow.ErrorLogoutFlowExpired)
	}

	return &lr, nil
}

func (p *Persister) FlushInactiveLoginConsentRequests(ctx context.Context, notAfter time.Time, limit int, batchSize int) (err error) {
	ctx, span := p.r.Tracer(ctx).Tracer().Start(ctx, "persistence.sql.FlushInactiveLoginConsentRequests")
	defer otelx.End(span, &err)

	/* #nosec G201 table is static */
	var f flow.Flow

	// The value of notAfter should be the minimum between input parameter and request max expire based on its configured age
	requestMaxExpire := time.Now().Add(-p.config.ConsentRequestMaxAge(ctx))
	if requestMaxExpire.Before(notAfter) {
		notAfter = requestMaxExpire
	}

	challenges := []string{}
	queryFormat := `
	SELECT login_challenge
	FROM hydra_oauth2_flow
	WHERE (
		(state != ?)
		OR (login_error IS NOT NULL AND login_error <> '{}' AND login_error <> '')
		OR (consent_error IS NOT NULL AND consent_error <> '{}' AND consent_error <> '')
	)
	AND requested_at < ?
	AND nid = ?
	ORDER BY login_challenge
	LIMIT %[1]d
	`

	// Select up to [limit] flows that can be safely deleted, i.e. flows that meet
	// the following criteria:
	// - flow.state is anything between FlowStateLoginInitialized and FlowStateConsentUnused (unhandled)
	// - flow.login_error has valid error (login rejected)
	// - flow.consent_error has valid error (consent rejected)
	// AND timed-out
	// - flow.requested_at < minimum of ttl.login_consent_request and notAfter
	q := p.Connection(ctx).RawQuery(fmt.Sprintf(queryFormat, limit), flow.FlowStateConsentUsed, notAfter, p.NetworkID(ctx))

	if err := q.All(&challenges); err == sql.ErrNoRows {
		return errors.Wrap(fosite.ErrNotFound, "")
	}

	// Delete in batch consent requests and their references in cascade
	for i := 0; i < len(challenges); i += batchSize {
		j := i + batchSize
		if j > len(challenges) {
			j = len(challenges)
		}

		q := p.Connection(ctx).RawQuery(
			fmt.Sprintf("DELETE FROM %s WHERE login_challenge in (?) AND nid = ?", (&f).TableName()),
			challenges[i:j],
			p.NetworkID(ctx),
		)

		if err := q.Exec(); err != nil {
			return sqlcon.HandleError(err)
		}
	}

	return nil
}

func (p *Persister) mySQLConfirmLoginSession(ctx context.Context, session *flow.LoginSession) error {
	err := sqlcon.HandleError(p.Connection(ctx).Create(session))
	if err == nil {
		return nil
	}

	if !errors.Is(err, sqlcon.ErrUniqueViolation) {
		return err
	}

	n, err := p.Connection(ctx).
		Where("id = ? and nid = ?", session.ID, session.NID).
		UpdateQuery(session, "authenticated_at", "subject", "identity_provider_session_id", "remember", "expires_at")
	if err != nil {
		return errors.WithStack(sqlcon.HandleError(err))
	}
	if n == 0 {
		return errorsx.WithStack(x.ErrNotFound)
	}

	return nil
}
