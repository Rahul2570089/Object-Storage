package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	defaultServerUrl = "http://localhost:8080"
	version          = "1.0.0"
)

type Config struct {
	ServerUrl string
	Verbose   bool
}

type BucketInfo struct {
	Name    string    `json:"name"`
	Created time.Time `json::"created"`
}

type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ContentType  string    `json:"content_type"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
}

type CLI struct {
	config *Config
	client *http.Client
}

func NewCLI(config *Config) *CLI {
	return &CLI{
		config: config,
		client: &http.Client{Timeout: 30 * time.Second},
	}
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
	case "ls", "list":
		return c.list(commandArgs)
	case "cp", "copy":
		return c.copy(commandArgs)
	case "rm", "remove":
		return c.remove(commandArgs)
	case "cat":
		return c.cat(commandArgs)
	case "stat":
		return c.stat(commandArgs)
	case "version":
		return c.showVersion()
	case "help", "--help", "-h":
		return c.showHelp()
	default:
		return fmt.Errorf("unknown command: %s\nRun 'storage-cli help' for usage information", command)
	}
}

func (c *CLI) showVersion() error {
	fmt.Printf("Storage CLI version %s\n", version)
	return nil
}

func (c *CLI) stat(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: storage-cli stat <bucket/object>")
	}

	remotePath := args[0]
	parts := strings.SplitN(remotePath, "/", 2)
	if len(parts) < 2 {
		return fmt.Errorf("path must be in format: bucket/object")
	}

	bucketName, objectKey := parts[0], parts[1]

	url := fmt.Sprintf("%s/objects/%s/%s", c.config.ServerUrl, bucketName, objectKey)
	resp, err := c.client.Head(url)
	if err != nil {
		resp, err := c.client.Get(url)
		if err != nil {
			return fmt.Errorf("failed to get objects info: %w", err)
		}
		resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("object not found")
	}

	fmt.Printf("Object: %s/%s\n", bucketName, objectKey)
	fmt.Printf("Content-Type: %s\n", resp.Header.Get("Content-Type"))
	fmt.Printf("Content-Length: %s\n", resp.Header.Get("Content-Length"))
	fmt.Printf("ETag: %s\n", resp.Header.Get("ETag"))
	fmt.Printf("Last-Modified: %s\n", resp.Header.Get("Last-Modified"))

	return nil
}

func (c *CLI) cat(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: storage-cli stat <bucket/object>")
	}

	remotePath := args[0]
	parts := strings.SplitN(remotePath, "/", 2)
	if len(parts) < 2 {
		return fmt.Errorf("path must be in format: bucket/object")
	}

	bucketName, objectKey := parts[0], parts[1]

	url := fmt.Sprintf("%s/objects/%s/%s", c.config.ServerUrl, bucketName, objectKey)
	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to get object: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get Object: %s", string(body))
	}

	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

func (c *CLI) remove(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: storage-cli rm <bucket/object>")
	}

	remotePath := args[0]
	parts := strings.SplitN(remotePath, "/", 2)
	if len(parts) < 2 {
		return fmt.Errorf("path must be in format: bucket/object")
	}

	bucketName, objectKey := parts[0], parts[1]

	if c.config.Verbose {
		fmt.Printf("Removing object '%s/%s'...\n", bucketName, objectKey)
	}

	url := fmt.Sprintf("%s/objects/%s/%s", c.config.ServerUrl, bucketName, objectKey)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete object: %s", string(body))
	}

	fmt.Printf("Object '%s/%s' removed successfully.\n", bucketName, objectKey)
	return nil

}

func (c *CLI) copy(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: storage-cli cp <source> <destination>\n" +
			"Examples:\n" +
			"  storage-cli cp file.txt mybucket/file.txt  # Upload local file\n" +
			"  storage-cli cp mybucket/file.txt file.txt  # Download to local file")
	}

	source := args[0]
	dest := args[1]

	if strings.Contains(source, "/") && !strings.Contains(dest, "/") {
		return c.downloadFile(source, dest)
	} else if !strings.Contains(source, "/") && strings.Contains(dest, "/") {
		return c.uploadFile(source, dest)
	} else {
		return fmt.Errorf("invalid copy operation. Use format: localfile bucket/object or bucket/object localfile")
	}
}

func (c *CLI) uploadFile(localPath, remotePath string) error {
	parts := strings.SplitN(remotePath, "/", 2)
	if len(parts) < 2 {
		return fmt.Errorf("remote path must be in format: bucket/object")
	}

	bucketName, objectKey := parts[0], parts[1]

	fileInfo, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("local file not found: %w", err)
	}

	if c.config.Verbose {
		fmt.Printf("Uploading '%s' to '%s/%s' (%s)...\n",
			localPath, bucketName, objectKey, formatSize(fileInfo.Size()))
	}

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}
	defer file.Close()

	contentType := getContentType(localPath)

	url := fmt.Sprintf("%s/objects/%s/%s", c.config.ServerUrl, bucketName, objectKey)
	req, err := http.NewRequest("PUT", url, file)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upload file: %s", string(body))
	}

	fmt.Printf("File uploaded successfully to '%s/%s'.\n", bucketName, objectKey)
	return nil
}

func (c *CLI) downloadFile(remotePath, localPath string) error {
	parts := strings.SplitN(remotePath, "/", 2)
	if len(parts) < 2 {
		return fmt.Errorf("remote path must be in format: bucket/object")
	}

	bucketName, objectKey := parts[0], parts[1]

	if c.config.Verbose {
		fmt.Printf("Downloading '%s/%s' to '%s'...\n", bucketName, objectKey, localPath)
	}

	url := fmt.Sprintf("%s/objects/%s/%s", c.config.ServerUrl, bucketName, objectKey)
	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to download file: %s", string(body))
	}

	localFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	defer localFile.Close()

	size, err := io.Copy(localFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	fmt.Printf("File downloaded successfully to '%s' (%s).\n", localPath, formatSize(size))
	return nil
}

func (c *CLI) list(args []string) error {
	if len(args) == 0 {
		return c.listBuckets()
	}

	bucketName := args[0]
	return c.listObjects(bucketName)
}

func (c *CLI) listBuckets() error {
	if c.config.Verbose {
		fmt.Println("Listing buckets...")
	}

	url := fmt.Sprintf("%s/buckets", c.config.ServerUrl)
	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to list buckets: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to list buckets: %s", string(body))
	}

	var buckets []BucketInfo
	if err := json.NewDecoder(resp.Body).Decode(&buckets); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if len(buckets) == 0 {
		fmt.Println("No buckets found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BUCKET NAME\tCREATED")
	fmt.Fprintln(w, "-----------\t-------")

	for _, bucket := range buckets {
		fmt.Fprintf(w, "%s\t%s\n", bucket.Name, bucket.Created.Format("2025-01-02 15:04:05"))
	}

	return w.Flush()
}

func (c *CLI) listObjects(bucketName string) error {
	if c.config.Verbose {
		fmt.Printf("Listing objects in bucket '%s'...\n", bucketName)
	}

	url := fmt.Sprintf("%s/objects/%s", c.config.ServerUrl, bucketName)
	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to list objects: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to list objects: %s", string(body))
	}

	var objects []ObjectInfo
	if err := json.NewDecoder(resp.Body).Decode(&objects); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if len(objects) == 0 {
		fmt.Printf("No objects found in bucket '%s'.\n", bucketName)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OBJECT KEY\tSIZE\tCONTENT TYPE\tLAST MODIFIED")
	fmt.Fprintln(w, "----------\t----\t------------\t-------------")

	for _, obj := range objects {
		sizeStr := formatSize(obj.Size)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			obj.Key, sizeStr, obj.ContentType,
			obj.LastModified.Format("2006-01-02 15:04:05"))
	}

	return w.Flush()
}

func (c *CLI) makeBucket(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: storage-cli mb <bucket-name>")
	}

	bucketName := args[0]

	if c.config.Verbose {
		fmt.Printf("Creating bucket '%s'...\n", bucketName)
	}

	url := fmt.Sprintf("%s/buckets/%s", c.config.ServerUrl, bucketName)
	req, err := http.NewRequest("PUT", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create bucket: %s", string(body))
	}

	fmt.Printf("Bucket '%s' created successfully.\n", bucketName)
	return nil
}

func (c *CLI) showHelp() error {
	fmt.Printf(`Storage CLI - Object Storage Client v%s

USAGE:
    storage-cli [OPTIONS] COMMAND [ARGS...]

OPTIONS:
    --server URL    Storage server URL (default: %s)
    --verbose, -v   Enable verbose output
    --help, -h      Show this help message

COMMANDS:
    mb, makebucket <bucket>           Create a new bucket
    ls, list [bucket]                 List buckets or objects in bucket
    cp, copy <source> <dest>          Upload or download files
    rm, remove <bucket/object>        Delete an object
    cat <bucket/object>               Display object content
    stat <bucket/object>              Show object information
    version                           Show version information
    help                              Show this help message

EXAMPLES:
    # Create a bucket
    storage-cli mb my-bucket

    # List all buckets
    storage-cli ls

    # List objects in a bucket
    storage-cli ls my-bucket

    # Upload a file
    storage-cli cp local-file.txt my-bucket/remote-file.txt

    # Download a file
    storage-cli cp my-bucket/remote-file.txt downloaded-file.txt

    # View file content
    storage-cli cat my-bucket/readme.txt

    # Get file information
    storage-cli stat my-bucket/data.json

    # Delete an object
    storage-cli rm my-bucket/old-file.txt

For more information, visit: https://github.com/yourusername/storage-cli
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
		help      = flag.Bool("help", false, "Show help message")
		h         = flag.Bool("h", false, "Show help message (short form)")
	)

	flag.Parse()

	config := &Config{
		ServerUrl: *serverURL,
		Verbose:   *verbose || *v,
	}

	cli := NewCLI(config)

	if *help || *h {
		cli.showHelp()
		return
	}

	args := flag.Args()
	if err := cli.Run(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
