package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
)

type httpError struct {
	Code int
	Err  error
}

// Error implements the error interface.
func (e *httpError) Error() string { return fmt.Sprintf("%d error: %+v", e.Code, e.Err) }

type ContextHandler func(context.Context, http.ResponseWriter, *http.Request, httprouter.Params) error

// ServeHTTP implements the http.Handler interface.
func (h ContextHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()
	err := h(ctx, w, r, httprouter.ParamsFromContext(ctx))
	if err == nil {
		log.Printf("%s: %s [%s]", r.Method, r.URL.Path, time.Since(start))
		return
	}
	switch err := err.(type) {
	case *httpError:
		http.Error(w, err.Err.Error(), err.Code)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	log.Printf("%s: %s [%s]: %s", r.Method, r.URL.Path, time.Since(start), err)
}

func registerHandlers(router *httprouter.Router, keep *TiddlyKeep, username, recipe string) {
	// TiddlyWiki index.html
	router.Handler(http.MethodGet, "/", ContextHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, p httprouter.Params) error {
		w.Header().Set("Content-Type", "text/html")
		return keep.GenerateIndex(ctx, w)
	}))
	router.Handler(http.MethodHead, "/", ContextHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, p httprouter.Params) error {
		w.Header().Set("Content-Type", "text/html")
		return nil
	}))

	// TiddlyWeb status
	router.Handler(http.MethodGet, "/status", ContextHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, p httprouter.Params) error {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"username": %q, "space": {"recipe": %q}}`, username, recipe)))
		return nil
	}))

	// List tiddlers in bag/recipe
	router.Handler(http.MethodGet, "/bags/:bag/tiddlers.json", ContextHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, p httprouter.Params) error {
		if err := r.ParseForm(); err != nil {
			return err
		}
		bag := p.ByName("bag")
		textFilter := NeverIncludeText
		if getFirst(r.Form["fat"]) == "1" {
			textFilter = AlwaysIncludeText
		}
		metadata, err := keep.ListBag(ctx, bag, textFilter)
		if err != nil {
			return fmt.Errorf("error retrieving skinny tiddler for bag %s: %v", bag, err)
		}
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(metadata)
	}))
	router.Handler(http.MethodGet, "/recipes/:recipe/tiddlers.json", ContextHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, p httprouter.Params) error {
		if err := r.ParseForm(); err != nil {
			return err
		}
		recipe := p.ByName("recipe")
		textFilter := NeverIncludeText
		if getFirst(r.Form["fat"]) == "1" {
			textFilter = AlwaysIncludeText
		}
		metadata, err := keep.ListRecipe(ctx, recipe, textFilter)
		if err != nil {
			return fmt.Errorf("error retrieving skinny tiddler for recipe %s: %v", recipe, err)
		}
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(metadata)
	}))

	// Retrieve tiddler from bag/recipe
	router.Handler(http.MethodGet, "/bags/:bag/tiddlers/*tiddler", ContextHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, p httprouter.Params) error {
		bag := p.ByName("bag")
		title := strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/bags/%s/tiddlers/", bag))
		ref := TiddlerRef{Title: title, Bag: bag}
		etag, err := constructETag(ctx, keep, &ref)
		if err != nil {
			return err
		}
		// TODO(schroederc): remove etag redundancy
		if check := r.Header.Get("If-None-Match"); check == etag {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
		t, err := keep.GetTiddler(ctx, ref, AlwaysIncludeText)
		if err != nil {
			return err
		}
		w.Header().Set("Etag", etag)
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(t)
	}))
	router.Handler(http.MethodGet, "/recipes/:recipe/tiddlers/*tiddler", ContextHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, p httprouter.Params) error {
		recipe := p.ByName("recipe")
		title := strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/recipes/%s/tiddlers/", recipe))
		ref := TiddlerRef{Title: title, Recipe: recipe}
		etag, err := constructETag(ctx, keep, &ref)
		if err != nil {
			return err
		}
		// TODO(schroederc): remove etag redundancy
		if check := r.Header.Get("If-None-Match"); check == etag {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
		t, err := keep.GetTiddler(ctx, ref, AlwaysIncludeText)
		if err != nil {
			return err
		}
		t.Recipe = recipe
		w.Header().Set("Etag", etag)
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(t)
	}))

	// Put tiddler into recipe
	router.Handler(http.MethodPut, "/recipes/:recipe/tiddlers/*tiddler", ContextHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, p httprouter.Params) error {
		recipe := p.ByName("recipe")
		tiddler := strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/recipes/%s/tiddlers/", recipe))
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			return err
		}

		var t Tiddler
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		t.Title = tiddler
		t.Recipe = recipe

		if err := keep.PutTiddler(ctx, &t); err != nil {
			return err
		}

		etag, err := constructETag(ctx, keep, &t.TiddlerRef)
		if err != nil {
			return err
		}
		w.Header().Set("Etag", etag)
		return nil
	}))

	// Delete tiddler from bag
	router.Handler(http.MethodDelete, "/bags/:bag/tiddlers/*tiddler", ContextHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, p httprouter.Params) error {
		bag := p.ByName("bag")
		tiddler := strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/bags/%s/tiddlers/", bag))
		return keep.DeleteTiddler(ctx, TiddlerRef{Title: tiddler, Bag: bag})
	}))
}

func constructETag(ctx context.Context, keep *TiddlyKeep, ref *TiddlerRef) (string, error) {
	if err := keep.ResolveRef(ctx, ref, false); err != nil {
		return "", err
	}
	return fmt.Sprintf("\"%s/%s/%d:%s\"", ref.Bag, url.QueryEscape(ref.Title), ref.Revision.Unix(), ref.Ref.String()), nil
}
