package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"strings"
	"time"

	"bitbucket.org/creachadair/stringset"
	"perkeep.org/pkg/blob"
	"perkeep.org/pkg/client"
	"perkeep.org/pkg/schema"
	"perkeep.org/pkg/search"
)

const tiddlyWikiIndex = "tiddlywiki.html"
const systemTiddlerPrefix = "$:/"

type TiddlyKeep struct {
	Client *client.Client

	IndexRef blob.Ref

	HideTiddlers, EmbedTiddlers string
}

var textEncoding = map[string]string{
	"text/vnd.tiddlywiki":                                                       "utf8",
	"application/x-tiddler":                                                     "utf8",
	"application/x-tiddlers":                                                    "utf8",
	"application/x-tiddler-html-div":                                            "utf8",
	"text/vnd.tiddlywiki2-recipe":                                               "utf8",
	"text/plain":                                                                "utf8",
	"text/css":                                                                  "utf8",
	"text/html":                                                                 "utf8",
	"application/hta":                                                           "utf16le",
	"application/javascript":                                                    "utf8",
	"application/json":                                                          "utf8",
	"application/pdf":                                                           "base64",
	"application/zip":                                                           "base64",
	"image/jpeg":                                                                "base64",
	"image/png":                                                                 "base64",
	"image/gif":                                                                 "base64",
	"image/svg+xml":                                                             "utf8",
	"image/x-icon":                                                              "base64",
	"application/font-woff":                                                     "base64",
	"application/x-font-ttf":                                                    "base64",
	"audio/ogg":                                                                 "base64",
	"video/mp4":                                                                 "base64",
	"audio/mp3":                                                                 "base64",
	"audio/mp4":                                                                 "base64",
	"text/markdown":                                                             "utf8",
	"text/x-markdown":                                                           "utf8",
	"application/enex+xml":                                                      "utf8",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "base64",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "base64",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "base64",
	"text/x-bibtex":            "utf8",
	"application/x-bibtex":     "utf8",
	"application/epub+zip":     "base64",
	"application/octet-stream": "base64",
}

func (k *TiddlyKeep) IndexFileNode(ctx context.Context) (blob.Ref, error) {
	if !k.IndexRef.Valid() {
		res, err := k.Client.Query(ctx, &search.SearchQuery{
			Constraint: &search.Constraint{
				Logical: &search.LogicalConstraint{
					Op: "and",
					A: &search.Constraint{
						Permanode: &search.PermanodeConstraint{
							Attr:  "title",
							Value: tiddlyWikiIndex,
						},
					},
					B: &search.Constraint{
						Permanode: &search.PermanodeConstraint{
							Attr:     "camliContent",
							NumValue: &search.IntConstraint{Min: 1},
						},
					},
				},
			},
		})
		if err != nil {
			return blob.Ref{}, err
		} else if len(res.Blobs) == 0 {
			return blob.Ref{}, fmt.Errorf("could not find %s", tiddlyWikiIndex)
		}
		k.IndexRef = res.Blobs[0].Blob
		log.Printf("Located %s: %s", tiddlyWikiIndex, k.IndexRef)
	}
	desc, err := k.Client.Describe(ctx, &search.DescribeRequest{
		BlobRef: k.IndexRef,
		Rules:   []*search.DescribeRule{{Attrs: []string{"camliContent"}}},
	})
	if err != nil {
		return blob.Ref{}, nil
	}
	file, ok := blob.Parse(getFirst(desc.Meta.Get(k.IndexRef).Permanode.Attr["camliContent"]))
	if !ok {
		return blob.Ref{}, fmt.Errorf("could not parse camliContent for permanode %s", k.IndexRef)
	}
	return file, nil
}

func (k *TiddlyKeep) getTiddlerPermanode(ctx context.Context, r TiddlerRef, createMissing bool) (blob.Ref, *search.DescribedBlob, error) {
	if r.Ref.Valid() {
		desc, err := k.Client.Describe(ctx, &search.DescribeRequest{
			BlobRef: r.Ref,
			Rules:   []*search.DescribeRule{{Attrs: tiddlerPermanodeAttrs}},
		})
		if err != nil {
			return blob.Ref{}, nil, err
		}
		return r.Ref, desc.Meta.Get(r.Ref), nil
	}

	if r.Title == "" {
		return blob.Ref{}, nil, errors.New("missing tiddler title")
	} else if r.Bag == "" && r.Recipe == "" {
		return blob.Ref{}, nil, fmt.Errorf("tiddler ref missing bag/recipe: %q", r.Title)
	}

	var bagConstraint *search.Constraint
	if r.Bag != "" {
		bagConstraint = &search.Constraint{
			Permanode: &search.PermanodeConstraint{
				Attr:  "tiddlerBag",
				Value: r.Bag,
			},
		}
	} else {
		switch r.Recipe {
		case "all", "system":
			bagConstraint = &search.Constraint{
				Permanode: &search.PermanodeConstraint{
					Attr:     "tiddlerBag",
					NumValue: &search.IntConstraint{Min: 1},
				},
			}
		default:
			return blob.Ref{}, nil, fmt.Errorf("recipe %q not supported", r.Recipe)
		}
	}

	res, err := k.searchTiddlers(ctx, &search.Constraint{
		Logical: &search.LogicalConstraint{
			Op: "and",
			A: &search.Constraint{
				Permanode: &search.PermanodeConstraint{
					Attr:  "title",
					Value: r.Title,
				},
			},
			B: bagConstraint,
		},
	}, true)
	if err == nil && len(res.Blobs) != 0 {
		b := res.Blobs[0].Blob
		return b, res.Describe.Meta.Get(b), nil
	} else if !createMissing {
		return blob.Ref{}, nil, fmt.Errorf("tiddler not found: %q", r.Title)
	}

	if r.Bag == "" {
		r.Bag = "default"
	}

	permaNode, err := k.Client.UploadNewPermanode(ctx)
	if err != nil {
		return blob.Ref{}, nil, err
	}
	_, err = k.Client.UploadAndSignBlob(ctx, schema.NewSetAttributeClaim(permaNode.BlobRef, "title", r.Title))
	if err != nil {
		return permaNode.BlobRef, nil, fmt.Errorf("error setting title: %v", err)
	}
	_, err = k.Client.UploadAndSignBlob(ctx, schema.NewSetAttributeClaim(permaNode.BlobRef, "tiddlerBag", r.Bag))
	if err != nil {
		return permaNode.BlobRef, nil, fmt.Errorf("error setting tiddlerBag: %v", err)
	}
	if k.HideTiddlers == "system" && strings.HasPrefix(r.Title, systemTiddlerPrefix) || k.HideTiddlers == "all" {
		_, err = k.Client.UploadAndSignBlob(ctx, schema.NewSetAttributeClaim(permaNode.BlobRef, "camliDefVis", "hide"))
		if err != nil {
			return permaNode.BlobRef, nil, fmt.Errorf("error setting camliDefVis: %v", err)
		}
	}
	return permaNode.BlobRef, nil, nil
}

func uploadBlob(ctx context.Context, c *client.Client, data []byte) (blob.Ref, error) {
	h := blob.NewHash()
	if _, err := h.Write(data); err != nil {
		return blob.Ref{}, err
	}
	u := &client.UploadHandle{
		BlobRef:  blob.RefFromHash(h),
		Contents: bytes.NewBuffer(data),
		Size:     uint32(len(data)),
	}
	put, err := c.Upload(ctx, u)
	if err != nil {
		return blob.Ref{}, err
	}
	return put.BlobRef, nil
}

func getFirst(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

func updateTags(ctx context.Context, c *client.Client, ref blob.Ref, existing, wanted stringset.Set) error {
	if wanted.Equals(existing) {
		return nil
	}

	if len(wanted) == 1 {
		_, err := c.UploadAndSignBlob(ctx, schema.NewSetAttributeClaim(ref, "tag", wanted.Elements()[0]))
		return err
	}

	for added := range wanted.Diff(existing) {
		if _, err := c.UploadAndSignBlob(ctx, schema.NewAddAttributeClaim(ref, "tag", added)); err != nil {
			return err
		}
	}
	for deleted := range existing.Diff(wanted) {
		if _, err := c.UploadAndSignBlob(ctx, schema.NewDelAttributeClaim(ref, "tag", deleted)); err != nil {
			return err
		}
	}
	return nil
}

// ResolveRef fully resolves a TiddlerRef, creating a missing Tiddler if requested.
func (k *TiddlyKeep) ResolveRef(ctx context.Context, r *TiddlerRef, createMissing bool) error {
	if r.Ref.Valid() && r.TextRef.Valid() && r.MetaRef.Valid() {
		return nil
	}

	ref, d, err := k.getTiddlerPermanode(ctx, *r, createMissing)
	if err != nil {
		return err
	}
	*r = constructTiddlerRef(ref, d)
	return err
}

// TODO remove
func (r *TiddlerRef) resolveRef(ctx context.Context, k *TiddlyKeep, createMissing bool) (blob.Ref, error) {
	if err := k.ResolveRef(ctx, r, createMissing); err != nil {
		return blob.Ref{}, err
	}
	return r.Ref, nil
}

// GenerateIndex implements part of the TiddlyServer interface.
func (k *TiddlyKeep) GenerateIndex(ctx context.Context, w io.Writer) error {
	ref, err := k.IndexFileNode(ctx)
	if err != nil {
		return err
	}
	b, err := k.Client.FetchSchemaBlob(ctx, ref)
	if err != nil {
		return fmt.Errorf("could not find index ref: %v", err)
	}
	rd, err := b.NewFileReader(k.Client)
	if err != nil {
		return err
	}

	if k.EmbedTiddlers != "all" && k.EmbedTiddlers != "system" {
		_, err = io.Copy(w, rd)
		return err
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rd); err != nil {
		return err
	}
	data := buf.Bytes()

	bootKernel := bytes.Index(data, []byte("<!--~~ Boot kernel ~~-->"))
	if bootKernel < 0 {
		return fmt.Errorf("could not find boot kernel")
	}

	w.Write(data[:bootKernel])
	io.WriteString(w, "\n<!--~~ Preloaded Tiddlers ~~-->\n")

	preloadStart := time.Now()
	preloadedTiddlers, err := k.ListRecipe(ctx, k.EmbedTiddlers, AlwaysIncludeText)
	if err != nil {
		return err
	}

	io.WriteString(w, "<script type='text/javascript'>\n$tw.preloadTiddlerArray(\n")
	if err := json.NewEncoder(w).Encode(preloadedTiddlers); err != nil {
		return err
	}
	io.WriteString(w, ");\n</script>\n\n")
	log.Printf("Wrote preloaded tiddlers in %s", time.Since(preloadStart))

	w.Write(data[bootKernel:])
	return nil
}

// PutTiddler implements part of the TiddlyServer interface.
func (k *TiddlyKeep) PutTiddler(ctx context.Context, t *Tiddler) error {
	if t.Bag == "" {
		t.Bag = "default"
	}

	obj, err := t.ToJSONObject()
	if err != nil {
		return err
	}

	delete(obj, "bag")
	delete(obj, "recipe")
	delete(obj, "revision")
	delete(obj, "tags")
	delete(obj, "text")
	delete(obj, "tiddlykeep-ref")

	meta, err := json.Marshal(obj)
	if err != nil {
		return err
	}

	var decoded []byte
	switch textEncoding[t.Type] {
	case "base64":
		decoded, err = base64.StdEncoding.DecodeString(t.Text)
		if err != nil {
			return err
		}
	default:
		decoded = []byte(t.Text)
	}

	textBlobRef, err := k.Client.UploadFile(ctx, "", bytes.NewReader(decoded), nil)
	if err != nil {
		return err
	}
	metaBlobRef, err := uploadBlob(ctx, k.Client, meta)
	if err != nil {
		return err
	}

	existing := TiddlerRef{Ref: t.Ref, Title: t.Title, Bag: t.Bag, Recipe: t.Recipe}
	if _, err := existing.resolveRef(ctx, k, true); err != nil {
		return err
	}

	if textBlobRef != existing.TextRef {
		if _, err := k.Client.UploadAndSignBlob(ctx, schema.NewSetAttributeClaim(existing.Ref, "camliContent", textBlobRef.String())); err != nil {
			return err
		}
	}
	if metaBlobRef != existing.MetaRef {
		if _, err := k.Client.UploadAndSignBlob(ctx, schema.NewSetAttributeClaim(existing.Ref, "tiddlerMeta", metaBlobRef.String())); err != nil {
			return err
		}
	}
	if err := updateTags(ctx, k.Client, existing.Ref, stringset.New(existing.Tags...), stringset.New(t.Tags...)); err != nil {
		return err
	}
	return nil
}

// DeleteTiddler implements part of the TiddlyServer interface.
func (k *TiddlyKeep) DeleteTiddler(ctx context.Context, ref TiddlerRef) error {
	blobRef, err := ref.resolveRef(ctx, k, false)
	if err != nil {
		return err
	}
	_, err = k.Client.UploadAndSignBlob(ctx, schema.NewDeleteClaim(blobRef))
	if err != nil {
		return err
	}
	return nil
}

// GetTiddler implements part of the TiddlyServer interface.
func (k *TiddlyKeep) GetTiddler(ctx context.Context, ref TiddlerRef, textFilter TextFilter) (*Tiddler, error) {
	if _, err := ref.resolveRef(ctx, k, false); err != nil {
		return nil, err
	} else if !ref.MetaRef.Valid() || ref.Title == "" {
		return nil, fmt.Errorf("%s tiddler not found", ref)
	}

	tiddler := &Tiddler{TiddlerMeta: TiddlerMeta{TiddlerRef: ref}}

	rd, _, err := k.Client.Fetch(ctx, ref.MetaRef)
	if err != nil {
		return nil, err
	}
	data, err := ioutil.ReadAll(rd)
	if err != nil {
		return nil, err
	}
	var tiddlerMeta map[string]interface{}
	if err := json.Unmarshal(data, &tiddlerMeta); err != nil {
		return nil, err
	}
	tiddler.MergeFrom(tiddlerMeta)

	if textFilter(tiddler) {
		b, err := k.Client.FetchSchemaBlob(ctx, ref.TextRef)
		if err != nil {
			return nil, err
		}
		rd, err := b.NewFileReader(k.Client)
		if err != nil {
			return nil, err
		}
		data, err := ioutil.ReadAll(rd)
		if err != nil {
			return nil, err
		}
		var encoded string
		switch textEncoding[tiddler.Type] {
		case "base64":
			encoded = base64.StdEncoding.EncodeToString(data)
		default:
			encoded = string(data)
		}
		tiddler.Text = encoded
	}

	return tiddler, nil
}

func constructTiddlerRef(ref blob.Ref, d *search.DescribedBlob) TiddlerRef {
	t := TiddlerRef{Ref: ref}
	if d != nil && d.Permanode != nil {
		t.Revision = d.Permanode.ModTime
		t.Title = getFirst(d.Permanode.Attr["title"])
		t.Bag = getFirst(d.Permanode.Attr["tiddlerBag"])
		t.MetaRef = blob.ParseOrZero(getFirst(d.Permanode.Attr["tiddlerMeta"]))
		t.TextRef = blob.ParseOrZero(getFirst(d.Permanode.Attr["camliContent"]))
		t.Tags = d.Permanode.Attr["tag"]
	}
	return t
}

func (k *TiddlyKeep) listRefs(ctx context.Context, res *search.SearchResult, textFilter TextFilter) ([]*Tiddler, error) {
	var meta search.MetaMap
	if desc := res.Describe; desc != nil {
		meta = desc.Meta
	}
	tiddlers := make([]*Tiddler, 0, len(res.Blobs))
	for _, b := range res.Blobs {
		t, err := k.GetTiddler(ctx, constructTiddlerRef(b.Blob, meta[b.Blob.String()]), textFilter)
		if err != nil {
			return nil, err
		}
		tiddlers = append(tiddlers, t)
	}

	return tiddlers, nil
}

// ListBag implements part of the TiddlyServer interface.
func (k *TiddlyKeep) ListBag(ctx context.Context, bag string, textFilter TextFilter) ([]*Tiddler, error) {
	// Find all Tiddlers within a bag
	res, err := k.searchTiddlers(ctx, &search.Constraint{
		Permanode: &search.PermanodeConstraint{
			Attr:  "tiddlerBag",
			Value: bag,
		},
	}, false)
	if err != nil {
		return nil, err
	}
	return k.listRefs(ctx, res, textFilter)
}

// ListRecipe implements part of the TiddlyServer interface.
func (k *TiddlyKeep) ListRecipe(ctx context.Context, recipe string, textFilter TextFilter) ([]*Tiddler, error) {
	if recipe != "all" {
	}

	// Find all Tiddlers within any bag
	anyBag := &search.Constraint{
		Permanode: &search.PermanodeConstraint{
			Attr:     "tiddlerBag",
			NumValue: &search.IntConstraint{Min: 1},
		},
	}

	var recipeConstraint *search.Constraint
	switch recipe {
	case "all":
		recipeConstraint = anyBag
	case "system":
		recipeConstraint = &search.Constraint{
			Logical: &search.LogicalConstraint{
				Op: "and",
				A:  anyBag,
				B: &search.Constraint{
					Permanode: &search.PermanodeConstraint{
						Attr:         "title",
						ValueMatches: &search.StringConstraint{HasPrefix: systemTiddlerPrefix},
					},
				},
			},
		}
	default:
		return nil, fmt.Errorf("unsupported recipe: %q", recipe)
	}

	res, err := k.searchTiddlers(ctx, recipeConstraint, false)
	if err != nil {
		return nil, err
	}
	return k.listRefs(ctx, res, textFilter)
}

func (k *TiddlyKeep) searchTiddlers(ctx context.Context, c *search.Constraint, includeMissingMeta bool) (*search.SearchResult, error) {
	if !includeMissingMeta {
		c = &search.Constraint{
			Logical: &search.LogicalConstraint{
				Op: "and",
				A: &search.Constraint{
					Permanode: &search.PermanodeConstraint{
						Attr:     "tiddlerMeta",
						NumValue: &search.IntConstraint{Min: 1},
					},
				},
				B: c,
			},
		}
	}

	return k.Client.Query(ctx, &search.SearchQuery{
		Constraint: c,
		Describe: &search.DescribeRequest{
			Rules: []*search.DescribeRule{{Attrs: tiddlerPermanodeAttrs}},
		},
	})
}

var tiddlerPermanodeAttrs = []string{"title", "tiddlerBag", "tiddlerMeta", "camliContent", "tag"}
