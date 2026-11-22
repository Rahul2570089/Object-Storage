package main

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultServerUrl = "http://localhost:8080"
	version          = "1.1.0"
)

type Config struct {
	ServerUrl string
	Verbose   bool
	Quiet     bool
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

type CLI struct {
	config *Config
	client *http.Client
}

func NewCLI(config *Config) *CLI {
	// No overall http.Client.Timeout: it would also cap the time to stream a
	// large body and kill big uploads/downloads. Only bound the wait for the
	// server's response headers.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &CLI{config: config, client: &http.Client{Transport: tr}}
}

// infof is progress/success output; --quiet silences it.
func (c *CLI) infof(format string, a ...any) {
	if !c.config.Quiet {
		fmt.Printf(format, a...)
	}
}

func (c *CLI) debugf(format string, a ...any) {
	if c.config.Verbose && !c.config.Quiet {
		fmt.Printf(format, a...)
	}
}

// errorf always prints, even with --quiet.
func (c *CLI) errorf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
}

func (c *CLI) Run(args []string) error {
	if len(args) == 0 {
		return c.showHelp()
	}

	command := args[0]
	commandArgs := args[1:]

	switch command {
	case "mb", "makebucket":
		return c.makeBucket(commandArgs)
	case "rb", "removebucket":
		return c.removeBucket(commandArgs)
	case "ls", "list":
		return c.list(commandArgs)
	case "cp", "copy":
		return c.copy(commandArgs)
	case "mv", "move":
		return c.move(commandArgs)
	case "rm", "remove":
		return c.remove(commandArgs)
	case "sync":
		return c.sync(commandArgs)
	case "cat":
		return c.cat(commandArgs)
	case "stat":
		return c.stat(commandArgs)
	case "version":
		return c.showVersion()
	case "help", "--help", "-h":
		return c.showHelp()
	default:
		return usagef("unknown command: %s\nRun 'storage-cli help' for usage information", command)
	}
}

func (c *CLI) showVersion() error {
	fmt.Printf("Storage CLI version %s\n", version)
	return nil
}

func (c *CLI) stat(args []string) error {
	if len(args) != 1 {
		return usagef("usage: storage-cli stat <bucket/object>")
	}
	r, err := parseRemote(args[0])
	if err != nil {
		return err
	}
	if r.Key == "" {
		return usagef("path must be in format: bucket/object")
	}

	resp, err := c.client.Head(c.objectURL(r.Bucket, r.Key))
	if err != nil {
		return fmt.Errorf("failed to get object info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("object '%s' not found", r)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to get object info: HTTP %d", resp.StatusCode)
	}

	fmt.Printf("Object: %s\n", r)
	fmt.Printf("Content-Type: %s\n", resp.Header.Get("Content-Type"))
	fmt.Printf("Content-Length: %s\n", resp.Header.Get("Content-Length"))
	fmt.Printf("ETag: %s\n", resp.Header.Get("ETag"))
	fmt.Printf("SHA-256: %s\n", resp.Header.Get("X-Content-SHA256"))
	fmt.Printf("Last-Modified: %s\n", resp.Header.Get("Last-Modified"))
	return nil
}

// cat streams to stdout while hashing. Output can't be un-written, so a
// checksum mismatch is reported after the fact via a non-zero exit code.
func (c *CLI) cat(args []string) error {
	if len(args) != 1 {
		return usagef("usage: storage-cli cat <bucket/object>")
	}
	r, err := parseRemote(args[0])
	if err != nil {
		return err
	}
	if r.Key == "" {
		return usagef("path must be in format: bucket/object")
	}

	resp, err := c.client.Get(c.objectURL(r.Bucket, r.Key))
	if err != nil {
		return fmt.Errorf("failed to get object: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("object '%s' not found", r)
	}
	if resp.StatusCode != http.StatusOK {
		return httpError(resp, "failed to get object")
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(os.Stdout, h), resp.Body); err != nil {
		return err
	}
	if want := resp.Header.Get("X-Content-SHA256"); want != "" && !strings.EqualFold(want, hexSum(h)) {
		return fmt.Errorf("checksum mismatch for '%s' (output was already written)", r)
	}
	return nil
}

func (c *CLI) makeBucket(args []string) error {
	if len(args) != 1 {
		return usagef("usage: storage-cli mb <bucket-name>")
	}
	bucketName := strings.TrimSuffix(strings.TrimPrefix(args[0], remoteScheme), "/")

	c.debugf("Creating bucket '%s'...\n", bucketName)

	req, err := http.NewRequest(http.MethodPut, c.bucketURL(bucketName), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return httpError(resp, "failed to create bucket")
	}

	c.infof("Bucket '%s' created successfully.\n", bucketName)
	return nil
}

// removeBucket needs DELETE /buckets/{name} on the server. The server refuses
// (409) if the bucket still holds objects, like `aws s3 rb` without --force.
func (c *CLI) removeBucket(args []string) error {
	if len(args) != 1 {
		return usagef("usage: storage-cli rb <bucket-name>")
	}
	bucketName := strings.TrimSuffix(strings.TrimPrefix(args[0], remoteScheme), "/")
	if bucketName == "" || strings.Contains(bucketName, "/") {
		return usagef("rb takes a bucket name only (use 'rm -r' to delete objects)")
	}

	c.debugf("Removing bucket '%s'...\n", bucketName)

	req, err := http.NewRequest(http.MethodDelete, c.bucketURL(bucketName), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to remove bucket: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
	case http.StatusNotFound:
		return fmt.Errorf("bucket '%s' not found", bucketName)
	case http.StatusConflict:
		return fmt.Errorf("bucket '%s' is not empty (delete its objects first: storage-cli rm -r %s)", bucketName, bucketName)
	default:
		return httpError(resp, "failed to remove bucket")
	}

	c.infof("Bucket '%s' removed.\n", bucketName)
	return nil
}

func (c *CLI) showHelp() error {
	fmt.Printf(`Storage CLI - Object Storage Client v%s

USAGE:
    storage-cli [OPTIONS] COMMAND [ARGS...]

OPTIONS (must come before the command):
    --server URL    Storage server URL (default: %s)
    --verbose, -v   Enable verbose output
    --quiet, -q     Only print errors (and command results such as ls/cat)
    --help, -h      Show this help message

COMMANDS:
    mb <bucket>                       Create a bucket
    rb <bucket>                       Remove an empty bucket
    ls [-r] [bucket[/prefix]]         List buckets, or objects (-r: recursive, flat)
    cp [-r] <source> <dest>           Upload, download or copy; -r for directories/prefixes
    mv [-r] <source> <dest>           Copy, then delete the source (only if the copy verified)
    rm [-r] <bucket/key|prefix>       Delete an object, or everything under a prefix with -r
    sync <source> <dest>              Transfer only new or changed files (size + MD5 ETag)
    cat <bucket/object>               Print object content (SHA-256 verified)
    stat <bucket/object>              Show object information
    version | help

    cp, mv, rm and sync also accept --dryrun to show what would happen.

PATHS:
    Local paths:  anything starting with ./ ../ or /, or that exists on disk.
    Bucket paths: bucket/key, or s3://bucket/key to force remote.
    For bucket-to-bucket copies, write the destination as s3://bucket/key.
    Recursive commands treat prefixes as folders: 'logs' matches logs/..., not logs2/...

EXIT CODES:
    0 success    1 one or more operations failed    2 usage error

EXAMPLES:
    storage-cli mb logs
    storage-cli cp -r ./var/log/app logs/app
    storage-cli sync ./var/log/app logs/app          # archive only new/changed files
    storage-cli ls -r logs/app
    storage-cli cp -r logs/app ./restore
    storage-cli mv logs/app/a.log s3://logs/old/a.log
    storage-cli rm -r --dryrun logs/app
    storage-cli rb logs
`, version, defaultServerUrl)
	return nil
}

func getContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))

	contentTypes := map[string]string{
		".txt":  "text/plain",
		".md":   "text/markdown",
		".html": "text/html",
		".css":  "text/css",
		".js":   "application/javascript",
		".json": "application/json",
		".xml":  "application/xml",
		".pdf":  "application/pdf",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".svg":  "image/svg+xml",
		".zip":  "application/zip",
		".tar":  "application/x-tar",
		".gz":   "application/gzip",
	}

	if contentType, exists := contentTypes[ext]; exists {
		return contentType
	}

	return "application/octet-stream"
}

func formatSize(size int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case size >= GB:
		return fmt.Sprintf("%.1fGB", float64(size)/GB)
	case size >= MB:
		return fmt.Sprintf("%.1fMB", float64(size)/MB)
	case size >= KB:
		return fmt.Sprintf("%.1fKB", float64(size)/KB)
	default:
		return fmt.Sprintf("%dB", size)
	}
}

func main() {
	var (
		serverURL = flag.String("server", defaultServerUrl, "Storage server URL")
		verbose   = flag.Bool("verbose", false, "Enable verbose output")
		v         = flag.Bool("v", false, "Enable verbose output (short form)")
		quiet     = flag.Bool("quiet", false, "Only print errors")
		q         = flag.Bool("q", false, "Only print errors (short form)")
		help      = flag.Bool("help", false, "Show help message")
		h         = flag.Bool("h", false, "Show help message (short form)")
	)

	flag.Parse()

	config := &Config{
		ServerUrl: strings.TrimRight(*serverURL, "/"),
		Verbose:   *verbose || *v,
		Quiet:     *quiet || *q,
	}

	cli := NewCLI(config)

	if *help || *h {
		cli.showHelp()
		return
	}

	if err := cli.Run(flag.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		var ue *usageError
		if errors.As(err, &ue) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
