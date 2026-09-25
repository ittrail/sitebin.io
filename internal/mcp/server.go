package mcp

import (
	"context"
	"fmt"
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Instructions is the server-level guidance sent to a client at initialize.
// It is the only place an agent is told how the pieces fit together before it
// starts calling tools, so it covers the two things that are not obvious from
// any single tool schema: what an edit id and edit password are, and that the
// password is shown exactly once.
const Instructions = `Sitebin publishes files as a live website. Drop files, get a URL.

Call create_site with your files to publish a site; the result carries the
public view_url, an edit_id, and — once, and never again — an edit_password.
Every other tool addresses a site by its edit_id.

Files travel as tool arguments, which suits pages, styles and scripts. For
anything larger than a few hundred KB — images, video, a whole build — call
open_upload and send the files with your own HTTP client, if you can make HTTP
requests (curl, or code execution with network access).

Authentication: if the connection carries an account API token, sites you
create belong to that account, list_sites shows them, and you do not need an
edit_password for any of them. A connection signed in through OAuth works the
same way, within the permissions it was granted. Without either you must pass
the edit_password returned at creation, and instances that require accounts
will refuse to create sites at all.

Sites are public to anyone with the URL. Do not publish secrets, credentials or
personal data, and do not create pages that imitate another organization's
sign-in or payment flow.`

// MaxRequestBytes is the most the transport reads of one request body.
//
// It must sit ABOVE the content limit, or the content limit is unreachable
// and callers get a bare 413 instead of the message that tells them to call
// open_upload. A request carrying MaxContentBytes of files is larger than
// that on the wire — base64 inflates by a third, and JSON string escaping can
// add more — so the transport is given twice the room and acts only as a
// backstop. The SDK's own default is 4 MiB, which is below our content cap and
// would have made this a silent trap. Exported because the HTTP layer peeks at
// the same bodies before the SDK does, and must read exactly as far.
const MaxRequestBytes = 2 * MaxContentBytes

// Info identifies this Sitebin instance to MCP clients.
type Info struct {
	// Name is the server name reported at initialize. Clients show it to the
	// user, so it should name the instance, not the software.
	Name string
	// Version is Sitebin's version string.
	Version string
}

// NewHandler returns the http.Handler serving the MCP endpoint.
//
// The transport is stateless: every request is authenticated on its own and the
// server keeps no session state. That is a deliberate choice for a hosted
// instance — there is nothing to lose across a restart, nothing to replicate
// between processes, and a request's authority is exactly the credential that
// request carried, never one an earlier request established. It also matches
// the sessionless direction of the MCP spec (SEP-2567). The cost is that
// server-initiated messages are unavailable, which a tools-only server has no
// use for.
func NewHandler(ops Ops, info Info) http.Handler {
	getServer := func(r *http.Request) *sdk.Server {
		return newServer(ops, info, ops.Authenticate(r))
	}
	return sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{
		Stateless: true,

		// See MaxRequestBytes: above the content limit, as a backstop.
		MaxRequestBodyBytes: MaxRequestBytes,

		// The SDK's DNS-rebinding guard refuses any request whose Host is not
		// a loopback name whenever the listener it arrived on is loopback.
		// That describes every normal Sitebin deployment: Caddy terminates
		// the public origin and proxies to the backend over 127.0.0.1 while
		// preserving the real Host, so the guard would 403 every genuine
		// request. It is aimed at a developer's localhost MCP server being
		// reached by a browser through DNS rebinding — a threat model in
		// which the loopback bind IS the security boundary. Here it is an
		// implementation detail behind a proxy.
		DisableLocalhostProtection: true,

		// What that guard was really protecting against — a web page driving
		// this endpoint from the user's browser — is handled properly instead.
		// CrossOriginProtection rejects cross-site *browser* requests on their
		// fetch metadata, and passes anything that sends none, which is every
		// real MCP client. Unlike fromOwnBrowser in the JSON API, this one is
		// a genuine boundary: it only ever refuses, and it refuses exactly the
		// population that cannot forge the headers.
		CrossOriginProtection: &http.CrossOriginProtection{},
	})
}

// addTool registers one tool from the catalog table: its title and hints
// come from its row, and so does the scope checked before its handler runs.
// The check stays inside the tool even though the HTTP layer already answers
// a missing scope with a 403, because that answer depends on parsing the
// request and this one does not.
//
// A tool with no row is a programming error, and panicking makes the first
// test that builds a server say so.
func addTool[In, Out any](s *sdk.Server, auth Auth, t *sdk.Tool, h sdk.ToolHandlerFor[In, Out]) {
	spec, ok := catalog[t.Name]
	if !ok {
		panic("mcp: tool " + t.Name + " has no row in the catalog")
	}
	t.Title = spec.title
	hints := spec.annotations
	hints.Title = spec.title
	t.Annotations = &hints
	sdk.AddTool(s, t, func(ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		if err := authorize(auth, spec.scope); err != nil {
			var none Out
			return nil, none, err
		}
		return h(ctx, req, in)
	})
}

// newServer builds the tool catalog bound to one caller's identity. Every
// handler closes over auth, so no tool can be called with an authority other
// than the one its request carried.
func newServer(ops Ops, info Info, auth Auth) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{
		Name:    info.Name,
		Version: info.Version,
	}, &sdk.ServerOptions{Instructions: Instructions})

	addTool(s, auth, &sdk.Tool{
		Name: "create_site",
		Description: "Publish files as a new website and return its public URL. " +
			"The returned edit_password is shown once and cannot be recovered — " +
			"record it, unless this connection uses an account API token, in which " +
			"case the token manages the site instead.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in createArgs) (*sdk.CallToolResult, *SiteResult, error) {
		files, err := DecodeFiles(in.Files)
		if err != nil {
			return nil, nil, err
		}
		return out(ops.CreateSite(ctx, auth, CreateInput{
			Files:    files,
			Settings: in.Settings,
			Domains:  in.Domains,
		}))
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "list_sites",
		Description: "List the sites the connected account owns. Requires an account: an account API token, or a connection signed in through OAuth.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, *listResult, error) {
		sites, err := ops.ListSites(ctx, auth)
		if err != nil {
			return nil, nil, err
		}
		return nil, &listResult{Sites: sites}, nil
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "get_site",
		Description: "Read a site's settings, usage and file list.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in siteArgs) (*sdk.CallToolResult, *SiteResult, error) {
		return out(ops.GetSite(ctx, auth, in.ref()))
	})

	addTool(s, auth, &sdk.Tool{
		Name: "update_site",
		Description: "Change a site's settings, including its name — a private label the owner sees in the account's " +
			"site list. Any field you omit is left alone. Setting name to an empty string removes it; setting " +
			"expires_at to an empty string clears the expiry, where the site's plan allows it.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in updateArgs) (*sdk.CallToolResult, *SiteResult, error) {
		return out(ops.UpdateSite(ctx, auth, in.ref(), in.Settings))
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "list_files",
		Description: "List the files in a site with their sizes.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in siteArgs) (*sdk.CallToolResult, *filesResult, error) {
		files, err := ops.ListFiles(ctx, auth, in.ref())
		if err != nil {
			return nil, nil, err
		}
		return nil, &filesResult{Files: files}, nil
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "read_file",
		Description: "Read one file from a site. Text files come back as text, binary files as base64.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in pathArgs) (*sdk.CallToolResult, *File, error) {
		f, err := ops.ReadFile(ctx, auth, in.ref(), in.Path)
		if err != nil {
			return nil, nil, err
		}
		return nil, &f, nil
	})

	addTool(s, auth, &sdk.Tool{
		Name: "write_files",
		Description: "Add or overwrite files in a site. With replace set, the site ends up containing " +
			"exactly the files you pass; the old files are removed only once every new one is written. " +
			"For files larger than a few hundred KB, use open_upload instead.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in writeArgs) (*sdk.CallToolResult, *SiteResult, error) {
		files, err := DecodeFiles(in.Files)
		if err != nil {
			return nil, nil, err
		}
		if len(files) == 0 && !in.Replace {
			return nil, nil, fmt.Errorf("no files to write: pass at least one file, or set replace to empty the site")
		}
		return out(ops.WriteFiles(ctx, auth, in.ref(), files, in.Replace))
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "delete_file",
		Description: "Delete one file from a site.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in pathArgs) (*sdk.CallToolResult, *SiteResult, error) {
		return out(ops.DeleteFile(ctx, auth, in.ref(), in.Path))
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "delete_site",
		Description: "Permanently delete a site and all its files. This cannot be undone.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in siteArgs) (*sdk.CallToolResult, *deleteResult, error) {
		if err := ops.DeleteSite(ctx, auth, in.ref()); err != nil {
			return nil, nil, err
		}
		return nil, &deleteResult{Status: "deleted", EditID: in.EditID}, nil
	})

	addTool(s, auth, &sdk.Tool{
		Name: "add_domain",
		Description: "Attach a custom domain to a site. Enterprise instances only. " +
			"The domain is attached once its DNS proves it belongs to this site: until then the result lists it under " +
			"pending_domains with the TXT record (or CNAME) to create. Create the record, then call add_domain again, " +
			"or wait: the instance re-checks pending domains itself.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in domainArgs) (*sdk.CallToolResult, *SiteResult, error) {
		return out(ops.AddDomain(ctx, auth, in.ref(), in.Domain))
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "remove_domain",
		Description: "Detach a custom domain from a site.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in domainArgs) (*sdk.CallToolResult, *SiteResult, error) {
		return out(ops.RemoveDomain(ctx, auth, in.ref(), in.Domain))
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "download_site",
		Description: "Download a site's files as a zip archive, returned as an attached resource.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in siteArgs) (*sdk.CallToolResult, any, error) {
		zip, err := ops.DownloadSite(ctx, auth, in.ref())
		if err != nil {
			return nil, nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.EmbeddedResource{
			Resource: &sdk.ResourceContents{
				URI:      "sitebin://" + in.EditID + ".zip",
				MIMEType: "application/zip",
				Blob:     zip,
			},
		}}}, nil, nil
	})

	addTool(s, auth, &sdk.Tool{
		Name: "open_upload",
		Description: "Get a short-lived token and URLs to upload files with your own HTTP client, for files " +
			"too large to pass to write_files (anything beyond a few hundred KB). Only useful if you can run " +
			"curl or make HTTP requests; otherwise use write_files. The token expires 5 minutes after its " +
			"last use, and after an hour at the latest; call open_upload again for a new one.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in siteArgs) (*sdk.CallToolResult, *UploadResult, error) {
		r, err := ops.OpenUpload(ctx, auth, in.ref())
		if err != nil {
			return nil, nil, err
		}
		return nil, r, nil
	})

	addTool(s, auth, &sdk.Tool{
		Name: "list_forms",
		Description: "List a site's email forms with their status and the HTML snippet for each. A form mails what visitors " +
			"submit to one recipient, who confirmed by email. Each submission arrives as a plain-text email (no HTML) from " +
			"the form's name: the fields in form order, then the visitor's files and a submission.json with the same data " +
			"attached.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in siteArgs) (*sdk.CallToolResult, *FormsResult, error) {
		return formsOut(ops.ListForms(ctx, auth, in.ref()))
	})

	addTool(s, auth, &sdk.Tool{
		Name: "add_form",
		Description: "Add an email form to a site. The recipient gets one email to confirm; until they click it the form is " +
			"pending and refuses every submission, so do not tell the user it works before then. Paste the returned " +
			"snippet into a page of the site with write_files. Needs an instance with form mail configured; plans limit " +
			"the forms per site. Each submission arrives as a plain-text email, not a designed one: the fields in form " +
			"order, the visitor's files and a submission.json attached. Tell the user to add the sender address to their " +
			"contacts or safe senders once confirmed, so a mail filter never holds a message back.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in formArgs) (*sdk.CallToolResult, *FormsResult, error) {
		return formsOut(ops.AddForm(ctx, auth, in.ref(), in.Form))
	})

	addTool(s, auth, &sdk.Tool{
		Name: "update_form",
		Description: "Change a form's name, recipient, captcha, attachments or thank-you page. A new recipient has to " +
			"confirm by email again before the form works.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in formUpdateArgs) (*sdk.CallToolResult, *FormsResult, error) {
		return formsOut(ops.UpdateForm(ctx, auth, in.ref(), in.Key, in.Form))
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "remove_form",
		Description: "Delete a form. Pages that still post to it get an error.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in formKeyArgs) (*sdk.CallToolResult, *FormsResult, error) {
		return formsOut(ops.RemoveForm(ctx, auth, in.ref(), in.Key))
	})

	addTool(s, auth, &sdk.Tool{
		Name:        "resend_form_confirmation",
		Description: "Email a form's recipient the confirmation link again, for a form that is pending or that its recipient stopped.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in formKeyArgs) (*sdk.CallToolResult, *FormsResult, error) {
		return formsOut(ops.ResendFormConfirmation(ctx, auth, in.ref(), in.Key))
	})

	return s
}

// ---- tool argument and result types ----
//
// The site-addressing fields are repeated rather than embedded because the
// SDK infers each tool's JSON schema from its argument struct, and an embedded
// struct would nest them under a field name the agent then has to get right.

type noArgs struct{}

type siteArgs struct {
	EditID       string `json:"edit_id" jsonschema:"the site's edit id, as returned by create_site or list_sites"`
	EditPassword string `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed when the connection uses an account API token that owns this site"`
}

func (a siteArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type pathArgs struct {
	EditID       string `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Path         string `json:"path" jsonschema:"the file's path inside the site, e.g. index.html or assets/app.js"`
}

func (a pathArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type domainArgs struct {
	EditID       string `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Domain       string `json:"domain" jsonschema:"the custom domain, e.g. docs.example.com"`
}

func (a domainArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type createArgs struct {
	Files    []File   `json:"files,omitempty" jsonschema:"the files to publish; may be empty, and filled later with write_files"`
	Settings Settings `json:"settings,omitempty" jsonschema:"optional settings for the new site"`
	Domains  []string `json:"custom_domains,omitempty" jsonschema:"custom domains to attach; enterprise instances only"`
}

type updateArgs struct {
	EditID       string   `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string   `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Settings     Settings `json:"settings" jsonschema:"the settings to change; omitted fields are left alone"`
}

func (a updateArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type writeArgs struct {
	EditID       string `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Files        []File `json:"files" jsonschema:"the files to write"`
	Replace      bool   `json:"replace,omitempty" jsonschema:"make the site contain exactly these files; the old ones are removed only once every new one is written"`
}

func (a writeArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type formArgs struct {
	EditID       string    `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string    `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Form         FormInput `json:"form" jsonschema:"the form's settings; name and recipient are required"`
}

func (a formArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type formKeyArgs struct {
	EditID       string `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Key          string `json:"key" jsonschema:"the form's key, as list_forms returns it"`
}

func (a formKeyArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type formUpdateArgs struct {
	EditID       string    `json:"edit_id" jsonschema:"the site's edit id"`
	EditPassword string    `json:"edit_password,omitempty" jsonschema:"the site's edit password; not needed with an owning account API token"`
	Key          string    `json:"key" jsonschema:"the form's key, as list_forms returns it"`
	Form         FormInput `json:"form" jsonschema:"the settings to change; omitted fields are left alone"`
}

func (a formUpdateArgs) ref() SiteRef { return SiteRef{EditID: a.EditID, EditPassword: a.EditPassword} }

type listResult struct {
	Sites []SiteSummary `json:"sites"`
}

type filesResult struct {
	Files []FileInfo `json:"files"`
}

type deleteResult struct {
	Status string `json:"status"`
	EditID string `json:"edit_id"`
}

// out adapts an (*SiteResult, error) pair to the SDK's three-value handler
// signature, so the tool bodies do not each repeat the same shuffle.
func out(r *SiteResult, err error) (*sdk.CallToolResult, *SiteResult, error) {
	if err != nil {
		return nil, nil, err
	}
	return nil, r, nil
}

func formsOut(r *FormsResult, err error) (*sdk.CallToolResult, *FormsResult, error) {
	if err != nil {
		return nil, nil, err
	}
	return nil, r, nil
}

func ptr[T any](v T) *T { return &v }
