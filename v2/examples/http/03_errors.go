package main

import (
	"errors"
	"net/http"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
)

// --- Example 3: errors a client can act on ----------------------------------
//
// Every layer returns core.IError. A handler returns it unchanged and the
// framework renders it:
//
//	HTTP/1.1 409 Conflict
//	{"code":"ARTICLE_LOCKED","message":"…","fields":{"locked_by":"u_42"}}
//
// `code` is the part a client writes an `if` against, which makes it as much of
// the API as the JSON schema. That is why the errors this service raises are
// declared once, in one place, instead of being spelled out at the call site
// where a typo is a silent behaviour change nothing catches.

var (
	// ErrArticleLocked is 409 rather than 400: nothing is wrong with what the
	// caller sent, the resource is simply in a state that refuses the change —
	// and the answer to a 409 is "retry later", not "fix your request".
	ErrArticleLocked = core.New(http.StatusConflict, "ARTICLE_LOCKED",
		"the article is being edited by someone else")

	// ErrQuotaReached carries the limit in Fields so the client can say what it
	// is, instead of hardcoding a number that changes with the price list.
	ErrQuotaReached = core.New(http.StatusForbidden, "ARTICLE_QUOTA_REACHED",
		"this plan does not allow more articles")
)

func mountArticleErrors(g *core.Group) {
	g.POST("/articles/:id/publish", publishArticle)
}

func publishArticle(c core.IHTTPContext) error {
	id := c.Param("id")

	if err := publish(c, id); err != nil {
		// errors.Is matches an *Error by code through any amount of wrapping,
		// so the handler can react to one specific failure without unwrapping
		// by hand or comparing strings.
		if errors.Is(err, ErrArticleLocked) {
			c.Log().Info("publish refused while locked", "article_id", id)
		}

		return err
	}

	return c.NoContent(http.StatusNoContent)
}

// publish is the service layer. It knows nothing about HTTP beyond the status
// its own errors already carry, which is what lets a job call it unchanged.
func publish(ctx core.IContext, id string) core.IError {
	found, err := loadArticle(ctx, id)
	if err != nil {
		// Wrap keeps the original status, code and fields and only adds where
		// the failure passed through — a 404 from the store is still a 404 at
		// the edge, and the message says which operation hit it.
		return core.Wrap(err, "publish article")
	}

	if found.LockedBy != "" {
		// The builders copy, so enriching a shared sentinel cannot corrupt it
		// for the next request. Never assign to one.
		return ErrArticleLocked.WithFields(map[string]any{"locked_by": found.LockedBy})
	}

	if err := countArticles(ctx); err != nil {
		// An infrastructure failure is the one case that needs ctx.NewError: it
		// reports to Sentry with this request's user, tags and breadcrumbs, and
		// answers the client with the generic message instead of the driver's.
		// Outside dev the cause never reaches the response.
		return ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

// lockedArticle is what the store returns — trimmed to what this file needs.
type lockedArticle struct {
	ID       string
	LockedBy string
}

// loadArticle stands in for the repository. Note what it returns for a missing
// row: NotFoundCustomError builds "ARTICLE_NOT_FOUND", a code specific enough
// for a client to branch on, from one call.
//
// A row that belongs to somebody else must answer 404 as well, never 403 — a
// 403 confirms the id exists, which is a fact the caller has no right to. Make
// ownership part of the WHERE clause and the two answers become identical for
// free, with no rule left for anyone to remember.
func loadArticle(_ core.IContext, id string) (*lockedArticle, core.IError) {
	switch id {
	case "0f7d0f5c-52b6-4c1b-9f5e-6f1c0f8f1a11":
		return &lockedArticle{ID: id}, nil
	case "11111111-1111-1111-1111-111111111111":
		return &lockedArticle{ID: id, LockedBy: "u_42"}, nil
	default:
		return nil, errmsgs.NotFoundCustomError("article")
	}
}

// countArticles stands in for a query that can fail for reasons the caller did
// nothing to cause.
func countArticles(ctx core.IContext) error {
	if ctx.DB() == nil {
		// Not an error worth reporting: this example runs without a database on
		// purpose. A real repository call would return the driver's error here,
		// and publish would hand it to ctx.NewError.
		return nil
	}

	return nil
}

// notFoundOrInternal is the shape most store lookups want at the edge: the
// caller's mistake stays theirs, everything else is reported as ours.
func notFoundOrInternal(ctx core.IContext, err error, resource string) core.IError {
	if errors.Is(err, errmsgs.NotFound) {
		return errmsgs.NotFoundCustomError(resource)
	}

	return ctx.NewError(err, errmsgs.InternalServerError)
}
