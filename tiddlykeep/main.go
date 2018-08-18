package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"

	"github.com/google/subcommands"
	"github.com/julienschmidt/httprouter"
	"perkeep.org/pkg/blob"
	"perkeep.org/pkg/client"
	"perkeep.org/pkg/schema"
)

// TODO(schroederc): initialize store with empty TiddlyWiki
const (
	emptyHTML       = "https://tiddlywiki.com/empty.html"
	pluginLibrary   = "https://tiddlywiki.com/library/v5.1.17/recipes/library/tiddlers/"
	tiddlyWebPlugin = "$:/plugins/tiddlywiki/tiddlyweb" // needs to be double URL-encoded
)

func main() {
	flag.Parse()
	cmd := subcommands.NewCommander(flag.CommandLine, "tiddlykeep")
	cmd.Register(&serveCommand{}, "")
	cmd.Register(&lsCommand{}, "")
	cmd.Register(&getCommand{}, "")
	cmd.Register(&editCommand{}, "")
	cmd.Register(&putCommand{}, "")
	cmd.Register(&deleteCommand{}, "")
	cmd.Register(&initCommand{}, "")
	if code := cmd.Execute(context.Background()); code != subcommands.ExitSuccess {
		os.Exit(int(code))
	}
}

type initCommand struct {
	Bag  string
	Keep TiddlyKeep
}

func (initCommand) Name() string     { return "init" }
func (initCommand) Synopsis() string { return "Initialize with empty TiddlyWiki" }
func (initCommand) Usage() string {
	return "[--hide_perkeep_nodes <none|system|all>] [--default_bag b]"
}
func (c *initCommand) SetFlags(flag *flag.FlagSet) {
	flag.StringVar(&c.Bag, "default_bag", "default", "")
	flag.StringVar(&c.Keep.HideTiddlers, "hide_perkeep_nodes", "system", "Whether to hide created Perkeep nodes {none all system}")
}
func (c *initCommand) Execute(ctx context.Context, flag *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	c.Keep.Client = client.NewOrFail()

	if _, err := c.Keep.IndexFileNode(ctx); err != nil {
		log.Printf("Fetching %s", emptyHTML)
		data, err := fetch(emptyHTML)
		if err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}

		ref, err := c.Keep.Client.UploadFile(ctx, emptyHTML, bytes.NewReader(data), nil)
		if err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}

		put, err := c.Keep.Client.UploadNewPermanode(ctx)
		if err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}

		if _, err := c.Keep.Client.UploadAndSignBlob(ctx, schema.NewSetAttributeClaim(put.BlobRef, "camliContent", ref.String())); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		} else if _, err := c.Keep.Client.UploadAndSignBlob(ctx, schema.NewSetAttributeClaim(put.BlobRef, "description", emptyHTML)); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		} else if _, err := c.Keep.Client.UploadAndSignBlob(ctx, schema.NewSetAttributeClaim(put.BlobRef, "title", tiddlyWikiIndex)); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
	}

	t, err := c.Keep.GetTiddler(ctx, TiddlerRef{Title: tiddlyWebPlugin, Recipe: "system"}, NeverIncludeText)
	if err == nil {
		log.Printf("TiddlyWeb plugin found: %s", t.Ref)
		return subcommands.ExitSuccess
	}
	log.Printf("Error finding TiddlyWeb plugin: %s; loading...", err)

	url := pluginLibrary + url.QueryEscape(url.QueryEscape(tiddlyWebPlugin)) + ".json"
	log.Printf("Fetching TiddlyWeb plugin from %s", url)
	data, err := fetch(url)
	if err != nil {
		log.Printf("ERROR: %+v", err)
		return subcommands.ExitFailure
	}

	var tiddler Tiddler
	if err := json.Unmarshal(data, &tiddler); err != nil {
		log.Printf("ERROR: %+v", err)
		return subcommands.ExitFailure
	} else if err := c.Keep.PutTiddler(ctx, &tiddler); err != nil {
		log.Printf("ERROR: %+v", err)
		return subcommands.ExitFailure
	}

	return subcommands.ExitSuccess
}

func fetch(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}

type putCommand struct {
	Bag  string
	Keep TiddlyKeep
}

func (putCommand) Name() string     { return "put" }
func (putCommand) Synopsis() string { return "Put Tiddlers" }
func (putCommand) Usage() string {
	return "[--hide_perkeep_nodes <none|system|all>] [--default_bag b] <tiddlers.json"
}
func (c *putCommand) SetFlags(flag *flag.FlagSet) {
	flag.StringVar(&c.Bag, "default_bag", "default", "")
	flag.StringVar(&c.Keep.HideTiddlers, "hide_perkeep_nodes", "system", "Whether to hide created Perkeep nodes {none all system}")
}
func (c *putCommand) Execute(ctx context.Context, flag *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	c.Keep.Client = client.NewOrFail()

	var tiddlers []*Tiddler
	if err := json.NewDecoder(os.Stdin).Decode(&tiddlers); err != nil {
		log.Printf("ERROR: %+v", err)
		return subcommands.ExitFailure
	}

	for _, t := range tiddlers {
		if t.Bag == "" {
			t.Bag = c.Bag
		}
		if err := c.Keep.PutTiddler(ctx, t); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
	}

	return subcommands.ExitSuccess
}

type deleteCommand struct {
	Bag, Recipe string
	ByRef       bool
}

func (deleteCommand) Name() string     { return "delete" }
func (deleteCommand) Synopsis() string { return "Delete Tiddlers by title" }
func (deleteCommand) Usage() string    { return "[--bag b|--recipe r] title+ | --by_ref ref+" }
func (c *deleteCommand) SetFlags(flag *flag.FlagSet) {
	flag.StringVar(&c.Bag, "bag", "", "")
	flag.StringVar(&c.Recipe, "recipe", "all", "")
	flag.BoolVar(&c.ByRef, "by_ref", false, "")
}
func (c *deleteCommand) Execute(ctx context.Context, flag *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	keep := &TiddlyKeep{Client: client.NewOrFail()}

	for _, title := range flag.Args() {
		ref := TiddlerRef{Bag: c.Bag, Recipe: c.Recipe}
		if c.ByRef {
			ref.Ref = blob.MustParse(title)
		} else {
			ref.Title = title
		}
		if err := keep.DeleteTiddler(ctx, ref); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
	}

	return subcommands.ExitSuccess
}

type getCommand struct {
	Bag, Recipe string
	ByRef       bool
}

func (getCommand) Name() string     { return "get" }
func (getCommand) Synopsis() string { return "Get Tiddlers by title" }
func (getCommand) Usage() string    { return "[--bag b|--recipe r] title+ | --by_ref ref+" }
func (c *getCommand) SetFlags(flag *flag.FlagSet) {
	flag.StringVar(&c.Bag, "bag", "", "")
	flag.StringVar(&c.Recipe, "recipe", "all", "")
	flag.BoolVar(&c.ByRef, "by_ref", false, "")
}
func (c *getCommand) Execute(ctx context.Context, flag *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	keep := &TiddlyKeep{Client: client.NewOrFail()}

	var tiddlers []*Tiddler
	for _, title := range flag.Args() {
		ref := TiddlerRef{Bag: c.Bag, Recipe: c.Recipe}
		if c.ByRef {
			ref.Ref = blob.MustParse(title)
		} else {
			ref.Title = title
		}
		t, err := keep.GetTiddler(ctx, ref, AlwaysIncludeText)
		if err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
		tiddlers = append(tiddlers, t)
	}

	if err := json.NewEncoder(os.Stdout).Encode(tiddlers); err != nil {
		log.Printf("ERROR: %+v", err)
		return subcommands.ExitFailure
	}

	return subcommands.ExitSuccess
}

type lsCommand struct {
	Fat         bool
	Recipe, Bag string
}

func (lsCommand) Name() string     { return "ls" }
func (lsCommand) Synopsis() string { return "List Tiddlers" }
func (lsCommand) Usage() string    { return "[--fat] [--bag b|--recipe r]" }
func (c *lsCommand) SetFlags(flag *flag.FlagSet) {
	flag.BoolVar(&c.Fat, "fat", false, "")
	flag.StringVar(&c.Bag, "bag", "", "")
	flag.StringVar(&c.Recipe, "recipe", "all", "")
}
func (c *lsCommand) Execute(ctx context.Context, flag *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	keep := &TiddlyKeep{Client: client.NewOrFail()}
	var (
		tiddlers   []*Tiddler
		err        error
		textFilter = NeverIncludeText
	)

	if c.Fat {
		textFilter = AlwaysIncludeText
	}

	if c.Recipe != "" {
		tiddlers, err = keep.ListRecipe(ctx, c.Recipe, textFilter)
	} else if c.Bag != "" {
		tiddlers, err = keep.ListBag(ctx, c.Bag, textFilter)
	} else {
		tiddlers, err = keep.ListRecipe(ctx, "all", textFilter)
	}

	if err != nil {
		log.Printf("ERROR: %+v", err)
		return subcommands.ExitFailure
	}

	if err := json.NewEncoder(os.Stdout).Encode(tiddlers); err != nil {
		log.Printf("ERROR: %+v", err)
		return subcommands.ExitFailure
	}

	return subcommands.ExitSuccess
}

type editCommand struct {
	Bag, Recipe string
	ByRef       bool
}

func (editCommand) Name() string     { return "edit" }
func (editCommand) Synopsis() string { return "Edit Tiddlers using $EDITOR" }
func (editCommand) Usage() string    { return "[--bag b|--recipe r] title+ | --by_ref ref+" }
func (c *editCommand) SetFlags(flag *flag.FlagSet) {
	flag.StringVar(&c.Bag, "bag", "", "")
	flag.StringVar(&c.Recipe, "recipe", "all", "")
	flag.BoolVar(&c.ByRef, "by_ref", false, "")
	// TODO(schroederc): support creating new Tiddlers
}
func (c *editCommand) Execute(ctx context.Context, flag *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	keep := &TiddlyKeep{Client: client.NewOrFail()}

	for _, title := range flag.Args() {
		ref := TiddlerRef{Bag: c.Bag, Recipe: c.Recipe}
		if c.ByRef {
			ref.Ref = blob.MustParse(title)
		} else {
			ref.Title = title
		}
		t, err := keep.GetTiddler(ctx, ref, AlwaysIncludeText)
		if err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
		tid, err := t.MarshalText()
		if err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
		f, err := ioutil.TempFile("", "tmp.*.tid")
		if err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
		defer os.Remove(f.Name())
		f.Write(tid)
		if err := f.Close(); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
		cmd := exec.Command("sh", "-c", "$EDITOR "+f.Name())
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
		data, err := ioutil.ReadFile(f.Name())
		if err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
		var new Tiddler
		if err := new.UnmarshalText(data); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
		new.Title = t.Title
		new.Bag = t.Bag
		new.Recipe = t.Recipe
		if err := os.Remove(f.Name()); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
		if err := keep.PutTiddler(ctx, &new); err != nil {
			log.Printf("ERROR: %+v", err)
			return subcommands.ExitFailure
		}
	}

	return subcommands.ExitSuccess
}

var currentUser string

func init() {
	currentUser = "GUEST"
	if u, err := user.Current(); err == nil && u.Username != "" {
		currentUser = u.Username
	}
}

type serveCommand struct {
	Address string

	Keep TiddlyKeep

	IndexRef string

	Username, Recipe string
}

func (serveCommand) Name() string     { return "serve" }
func (serveCommand) Synopsis() string { return "TiddlyWeb-compatible server" }
func (serveCommand) Usage() string {
	return "[--embed <none|system|all>] [--hide_perkeep_nodes <none|system|all>] [--index_ref sha] [--listen addr]"
}
func (c *serveCommand) SetFlags(flag *flag.FlagSet) {
	flag.StringVar(&c.Address, "listen", "localhost:8080", "HTTP listening address")
	flag.StringVar(&c.IndexRef, "index_ref", "", "")
	flag.StringVar(&c.Username, "user", currentUser, "")
	flag.StringVar(&c.Recipe, "recipe", "all", "")
	flag.StringVar(&c.Keep.HideTiddlers, "hide_perkeep_nodes", "system", "Whether to hide created Perkeep nodes {none all system}")
	flag.StringVar(&c.Keep.EmbedTiddlers, "embed", "system", "Tiddlers to embed {none all system}")
}
func (c *serveCommand) Execute(ctx context.Context, flag *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	router := httprouter.New()
	c.Keep.Client = client.NewOrFail()
	if c.IndexRef != "" {
		c.Keep.IndexRef = blob.MustParse(c.IndexRef)
	}
	registerHandlers(router, &c.Keep, c.Username, c.Recipe)
	log.Printf("Starting HTTP server on %s", c.Address)
	if err := http.ListenAndServe(c.Address, router); err != nil {
		log.Fatal(err)
	}
	return subcommands.ExitSuccess
}
