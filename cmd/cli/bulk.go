package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

type opts struct{ recursive, dryRun bool }

func parseOpts(args []string) (opts, []string, error) {
	var o opts
	var rest []string
	done := false
	for _, a := range args {
		if done || !strings.HasPrefix(a, "-") || a == "-" {
			rest = append(rest, a)
			continue
		}
		switch a {
		case "--":
			done = true
		case "-r", "-R", "--recursive":
			o.recursive = true
		case "--dryrun", "--dry-run":
			o.dryRun = true
		default:
			return o, nil, usagef("unknown option: %s", a)
		}
	}
	return o, rest, nil
}

type job struct {
	desc string
	run  func() error
}

func failJob(desc string, err error) job {
	return job{desc: desc, run: func() error { return err }}
}

func (c *CLI) runJobs(jobs []job, dryRun bool) error {
	failed := 0
	for _, j := range jobs {
		if dryRun {
			fmt.Printf("(dryrun) %s\n", j.desc)
			continue
		}
		if err := j.run(); err != nil {
			failed++
			c.errorf("failed: %s: %v\n", j.desc, err)
			continue
		}
		c.infof("%s\n", j.desc)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d operations failed", failed, len(jobs))
	}
	return nil
}

func (c *CLI) uploadJob(local string, dst remotePath, move bool) job {
	verb := "upload"
	if move {
		verb = "move"
	}
	return job{desc: fmt.Sprintf("%s: %s -> %s", verb, local, dst), run: func() error {
		if err := c.uploadFile(local, dst); err != nil {
			return err
		}
		if move {
			if err := os.Remove(local); err != nil {
				return fmt.Errorf("uploaded, but failed to remove source: %w", err)
			}
		}
		return nil
	}}
}

func (c *CLI) downloadJob(src remotePath, local string, move bool) job {
	verb := "download"
	if move {
		verb = "move"
	}
	return job{desc: fmt.Sprintf("%s: %s -> %s", verb, src, local), run: func() error {
		if _, err := c.downloadObject(src, local); err != nil {
			return err
		}
		if move {
			return c.deleteObject(src)
		}
		return nil
	}}
}

func (c *CLI) copyJob(src, dst remotePath, move bool) job {
	verb := "copy"
	if move {
		verb = "move"
	}
	return job{desc: fmt.Sprintf("%s: %s -> %s", verb, src, dst), run: func() error {
		if err := c.copyRemote(src, dst); err != nil {
			return err
		}
		if move {
			return c.deleteObject(src)
		}
		return nil
	}}
}

func (c *CLI) deleteJob(r remotePath) job {
	return job{desc: "delete: " + r.String(), run: func() error { return c.deleteObject(r) }}
}

type endpoint struct {
	local  string
	remote *remotePath
}

func explicitLocal(p string) bool {
	return p == "." || p == ".." || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") ||
		strings.HasPrefix(p, `.\`) || strings.HasPrefix(p, `..\`) ||
		filepath.IsAbs(p) || filepath.VolumeName(p) != ""
}

func existsLocal(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func classifyPair(src, dst string) (endpoint, endpoint, error) {
	var s, d endpoint
	srcLocal := !strings.HasPrefix(src, remoteScheme) && (explicitLocal(src) || existsLocal(src))
	dstForcedRemote := strings.HasPrefix(dst, remoteScheme)

	if srcLocal {
		if !dstForcedRemote && explicitLocal(dst) {
			return s, d, usagef("one side must be a bucket path (local-to-local copies are not supported)")
		}
		s.local = src
		r, err := parseRemote(dst)
		if err != nil {
			return s, d, err
		}
		d.remote = &r
		return s, d, nil
	}

	r, err := parseRemote(src)
	if err != nil {
		return s, d, err
	}
	s.remote = &r

	dstLocal := !dstForcedRemote && (explicitLocal(dst) || !strings.Contains(dst, "/") || isDir(dst))
	if dstLocal {
		d.local = dst
		return s, d, nil
	}
	r2, err := parseRemote(dst)
	if err != nil {
		return s, d, err
	}
	d.remote = &r2
	return s, d, nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func (c *CLI) list(args []string) error {
	o, rest, err := parseOpts(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: storage-cli ls [-r] [bucket[/prefix]]")
	}

	if len(rest) == 0 {
		if !o.recursive {
			return c.listBuckets()
		}
		buckets, err := c.fetchBuckets()
		if err != nil {
			return err
		}
		var all []ObjectInfo
		for _, b := range buckets {
			objs, err := c.fetchObjects(b.Name)
			if err != nil {
				return err
			}
			for _, ob := range objs {
				ob.Key = b.Name + "/" + ob.Key
				all = append(all, ob)
			}
		}
		return printObjects(all, "No objects found.")
	}

	r, err := parseRemote(rest[0])
	if err != nil {
		return err
	}
	objs, err := c.fetchObjects(r.Bucket)
	if err != nil {
		return err
	}
	if o.recursive {
		var out []ObjectInfo
		for _, ob := range objs {
			if strings.HasPrefix(ob.Key, r.Key) { // raw prefix, like aws s3 ls
				out = append(out, ob)
			}
		}
		return printObjects(out, fmt.Sprintf("No objects found under '%s'.", r))
	}
	return printLevel(objs, r)
}

func printLevel(objs []ObjectInfo, r remotePath) error {
	prefixes := map[string]struct{}{}
	var files []ObjectInfo
	for _, ob := range objs {
		if !strings.HasPrefix(ob.Key, r.Key) {
			continue
		}
		restKey := ob.Key[len(r.Key):]
		if i := strings.Index(restKey, "/"); i >= 0 {
			prefixes[r.Key+restKey[:i+1]] = struct{}{}
		} else {
			files = append(files, ob)
		}
	}
	if len(prefixes) == 0 && len(files) == 0 {
		fmt.Printf("No objects found under '%s'.\n", r)
		return nil
	}
	names := make([]string, 0, len(prefixes))
	for p := range prefixes {
		names = append(names, p)
	}
	sort.Strings(names)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OBJECT KEY\tSIZE\tCONTENT TYPE\tLAST MODIFIED")
	fmt.Fprintln(w, "----------\t----\t------------\t-------------")
	for _, p := range names {
		fmt.Fprintf(w, "%s\tPRE\t\t\n", p)
	}
	for _, ob := range files {
		writeObjectRow(w, ob)
	}
	return w.Flush()
}

func printObjects(objs []ObjectInfo, emptyMsg string) error {
	if len(objs) == 0 {
		fmt.Println(emptyMsg)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OBJECT KEY\tSIZE\tCONTENT TYPE\tLAST MODIFIED")
	fmt.Fprintln(w, "----------\t----\t------------\t-------------")
	for _, ob := range objs {
		writeObjectRow(w, ob)
	}
	return w.Flush()
}

func writeObjectRow(w *tabwriter.Writer, ob ObjectInfo) {
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ob.Key, formatSize(ob.Size), ob.ContentType,
		ob.LastModified.Format("2006-01-02 15:04:05"))
}

func (c *CLI) listBuckets() error {
	c.debugf("Listing buckets...\n")
	buckets, err := c.fetchBuckets()
	if err != nil {
		return err
	}
	if len(buckets) == 0 {
		fmt.Println("No buckets found.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BUCKET NAME\tCREATED")
	fmt.Fprintln(w, "-----------\t-------")
	for _, b := range buckets {
		fmt.Fprintf(w, "%s\t%s\n", b.Name, b.Created.Format("2006-01-02 15:04:05"))
	}
	return w.Flush()
}

func (c *CLI) remove(args []string) error {
	o, rest, err := parseOpts(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: storage-cli rm [-r] [--dryrun] <bucket/key | bucket/prefix>")
	}
	r, err := parseRemote(rest[0])
	if err != nil {
		return err
	}

	if !o.recursive {
		if r.Key == "" {
			return usagef("rm needs bucket/key (use -r to delete everything under a bucket or prefix, rb to remove an empty bucket)")
		}
		return c.runJobs([]job{c.deleteJob(r)}, o.dryRun)
	}

	objs, err := c.fetchUnder(r)
	if err != nil {
		return err
	}
	jobs := make([]job, 0, len(objs))
	for _, ob := range objs {
		jobs = append(jobs, c.deleteJob(remotePath{r.Bucket, ob.Key}))
	}
	return c.runJobs(jobs, o.dryRun)
}

func (c *CLI) copy(args []string) error { return c.transfer("cp", args, false) }
func (c *CLI) move(args []string) error { return c.transfer("mv", args, true) }

func (c *CLI) transfer(cmd string, args []string, move bool) error {
	o, rest, err := parseOpts(args)
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return usagef("usage: storage-cli %s [-r] [--dryrun] <source> <destination>\n"+
			"  storage-cli %s file.txt bucket/key        # upload\n"+
			"  storage-cli %s bucket/key file.txt        # download\n"+
			"  storage-cli %s -r ./dir bucket/prefix     # bulk upload\n"+
			"  storage-cli %s -r bucket/prefix ./dir     # bulk download\n"+
			"  storage-cli %s bucket/a s3://bucket2/b    # bucket to bucket", cmd, cmd, cmd, cmd, cmd, cmd)
	}
	src, dst, err := classifyPair(rest[0], rest[1])
	if err != nil {
		return err
	}

	var jobs []job
	if o.recursive {
		jobs, err = c.planRecursive(src, dst, move)
	} else {
		jobs, err = c.planSingle(src, dst, move)
	}
	if err != nil {
		return err
	}
	return c.runJobs(jobs, o.dryRun)
}

func (c *CLI) planSingle(src, dst endpoint, move bool) ([]job, error) {
	switch {
	case src.local != "":
		fi, err := os.Stat(src.local)
		if err != nil {
			return nil, fmt.Errorf("local file not found: %w", err)
		}
		if fi.IsDir() {
			return nil, usagef("'%s' is a directory (use -r)", src.local)
		}
		key := dst.remote.Key
		if key == "" || strings.HasSuffix(key, "/") {
			key += filepath.Base(src.local)
		}
		return []job{c.uploadJob(src.local, remotePath{dst.remote.Bucket, key}, move)}, nil

	case dst.local != "":
		if src.remote.Key == "" {
			return nil, usagef("source must be bucket/key (use -r for a bucket or prefix)")
		}
		return []job{c.downloadJob(*src.remote, resolveLocalDest(dst.local, src.remote.Key), move)}, nil

	default:
		if src.remote.Key == "" {
			return nil, usagef("source must be bucket/key (use -r for a bucket or prefix)")
		}
		key := dst.remote.Key
		if key == "" || strings.HasSuffix(key, "/") {
			key += path.Base(src.remote.Key)
		}
		return []job{c.copyJob(*src.remote, remotePath{dst.remote.Bucket, key}, move)}, nil
	}
}

func resolveLocalDest(dest, key string) string {
	if isDir(dest) || strings.HasSuffix(dest, "/") || strings.HasSuffix(dest, string(os.PathSeparator)) {
		return filepath.Join(dest, path.Base(key))
	}
	return dest
}

func (c *CLI) planRecursive(src, dst endpoint, move bool) ([]job, error) {
	var jobs []job

	switch {
	case src.local != "": // directory -> bucket/prefix
		if !isDir(src.local) {
			return nil, usagef("'%s' is not a directory", src.local)
		}
		err := filepath.WalkDir(src.local, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				c.errorf("warning: skipping non-regular file %s\n", p)
				return nil
			}
			rel, err := filepath.Rel(src.local, p)
			if err != nil {
				return err
			}
			target := remotePath{dst.remote.Bucket, joinKey(dst.remote.Key, filepath.ToSlash(rel))}
			jobs = append(jobs, c.uploadJob(p, target, move))
			return nil
		})
		return jobs, err

	case dst.local != "": // bucket/prefix -> directory
		objs, err := c.fetchUnder(*src.remote)
		if err != nil {
			return nil, err
		}
		for _, ob := range objs {
			from := remotePath{src.remote.Bucket, ob.Key}
			rel := relTo(ob.Key, src.remote.Key)
			if !filepath.IsLocal(filepath.FromSlash(rel)) {
				jobs = append(jobs, failJob("download: "+from.String(), fmt.Errorf("refusing unsafe key")))
				continue
			}
			jobs = append(jobs, c.downloadJob(from, filepath.Join(dst.local, filepath.FromSlash(rel)), move))
		}
		return jobs, nil

	default: // bucket/prefix -> bucket/prefix
		objs, err := c.fetchUnder(*src.remote)
		if err != nil {
			return nil, err
		}
		for _, ob := range objs {
			from := remotePath{src.remote.Bucket, ob.Key}
			to := remotePath{dst.remote.Bucket, joinKey(dst.remote.Key, relTo(ob.Key, src.remote.Key))}
			jobs = append(jobs, c.copyJob(from, to, move))
		}
		return jobs, nil
	}
}

// sync transfers only files that are missing or differ in size or MD5. It
// never deletes anything.
func (c *CLI) sync(args []string) error {
	o, rest, err := parseOpts(args)
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return usagef("usage: storage-cli sync [--dryrun] <dir> <bucket/prefix>  |  <bucket/prefix> <dir>")
	}
	src, dst, err := classifyPair(rest[0], rest[1])
	if err != nil {
		return err
	}

	var jobs []job
	skipped := 0
	switch {
	case src.local != "" && dst.remote != nil:
		jobs, skipped, err = c.planSyncUp(src.local, *dst.remote)
	case src.remote != nil && dst.local != "":
		jobs, skipped, err = c.planSyncDown(*src.remote, dst.local)
	default:
		return usagef("sync needs one local directory and one bucket path")
	}
	if err != nil {
		return err
	}

	c.infof("sync: %d to transfer, %d already up to date\n", len(jobs), skipped)
	return c.runJobs(jobs, o.dryRun)
}

func (c *CLI) planSyncUp(localDir string, dst remotePath) ([]job, int, error) {
	if !isDir(localDir) {
		return nil, 0, usagef("'%s' is not a directory", localDir)
	}
	remoteObjs, err := c.fetchUnder(dst)
	if err != nil {
		return nil, 0, err
	}
	existing := make(map[string]ObjectInfo, len(remoteObjs))
	for _, ob := range remoteObjs {
		existing[relTo(ob.Key, dst.Key)] = ob
	}

	var jobs []job
	skipped := 0
	err = filepath.WalkDir(localDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			c.errorf("warning: skipping non-regular file %s\n", p)
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(localDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		target := remotePath{dst.Bucket, joinKey(dst.Key, rel)}

		if ob, ok := existing[rel]; ok {
			same, herr := sameContent(p, fi.Size(), ob)
			if herr != nil {
				jobs = append(jobs, failJob("upload: "+p, herr))
				return nil
			}
			if same {
				skipped++
				c.debugf("skip: %s (unchanged)\n", p)
				return nil
			}
		}
		jobs = append(jobs, c.uploadJob(p, target, false))
		return nil
	})
	return jobs, skipped, err
}

func (c *CLI) planSyncDown(src remotePath, localDir string) ([]job, int, error) {
	objs, err := c.fetchUnder(src)
	if err != nil {
		return nil, 0, err
	}
	var jobs []job
	skipped := 0
	for _, ob := range objs {
		from := remotePath{src.Bucket, ob.Key}
		rel := relTo(ob.Key, src.Key)
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			jobs = append(jobs, failJob("download: "+from.String(), fmt.Errorf("refusing unsafe key")))
			continue
		}
		localPath := filepath.Join(localDir, filepath.FromSlash(rel))
		if fi, err := os.Stat(localPath); err == nil && fi.Mode().IsRegular() {
			same, herr := sameContent(localPath, fi.Size(), ob)
			if herr != nil {
				jobs = append(jobs, failJob("download: "+from.String(), herr))
				continue
			}
			if same {
				skipped++
				c.debugf("skip: %s (unchanged)\n", localPath)
				continue
			}
		}
		jobs = append(jobs, c.downloadJob(from, localPath, false))
	}
	return jobs, skipped, nil
}
