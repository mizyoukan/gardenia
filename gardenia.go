package main

import (
	"github.com/mitchellh/go-homedir"

	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"text/template"
)

var cacheDir = flag.String("c", "~/.cache/gardenia", "Cache directory path")
var clean = flag.Bool("e", false, "Clean not managed plugins")
var force = flag.Bool("f", false, "Force reinstall plugins")
var list = flag.Bool("l", false, "Only list plugins to install")

var vimfilesDir string
var cacheDownloadDir string

func vimfiles() string {
	home, err := homedir.Dir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "vimfiles")
	}
	return filepath.Join(home, ".vim")
}

const (
	listBranchesURL string = "https://api.github.com/repos/{{.Owner}}/{{.Repo}}/branches"
	downloadURL     string = "https://github.com/{{.Owner}}/{{.Repo}}/archive/{{.Branch}}.zip"
)

type repoGetParam struct {
	Owner  string
	Repo   string
	Branch string
}

type branchesResponseCommit struct {
	SHA string `json:"sha"`
	URL string `json:"url"`
}

type branchesResponse struct {
	Name   string                 `json:"name"`
	Commit branchesResponseCommit `json:"commit"`
}

func listBranches(owner, repo string) (resp []branchesResponse, err error) {
	tmpl, err := template.New("listBranchesURL").Parse(listBranchesURL)
	if err != nil {
		return
	}

	var url bytes.Buffer
	param := repoGetParam{Owner: owner, Repo: repo}
	if err = tmpl.Execute(&url, param); err != nil {
		return
	}

	httpResp, err := http.Get(url.String())
	if err != nil {
		return
	}
	defer httpResp.Body.Close()

	err = json.NewDecoder(httpResp.Body).Decode(&resp)
	return
}

func download(owner, repo, branch, dest string) (err error) {
	tmpl, err := template.New("downloadURL").Parse(downloadURL)
	if err != nil {
		return
	}

	var url bytes.Buffer
	param := repoGetParam{Owner: owner, Repo: repo, Branch: branch}
	if err = tmpl.Execute(&url, param); err != nil {
		return
	}

	resp, err := http.Get(url.String())
	if err != nil {
		return
	}
	defer resp.Body.Close()

	file, err := os.Create(dest)
	if err != nil {
		return
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	return
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()

		path := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.Mode())
		} else {
			fc, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return err
			}
			defer fc.Close()

			if _, err = io.Copy(fc, rc); err != nil {
				return err
			}
		}
	}

	return nil
}

type bundle struct {
	Dir   string
	Owner string
	Repo  string
}

func newBundle(ownerrepo, dir string) (b bundle, err error) {
	sp := strings.SplitN(ownerrepo, "/", 2)
	if len(sp) != 2 {
		err = fmt.Errorf("%s: plugin should be style of :owner/:repo", ownerrepo)
		return
	}

	b = bundle{Dir: dir, Owner: sp[0], Repo: sp[1]}
	return
}

func parseConfig(file string) (bundles []bundle, err error) {
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()

	var data interface{}
	if err = json.NewDecoder(f).Decode(&data); err != nil {
		return
	}

	bundles = make([]bundle, 0)

	var fn func(interface{}, string) error
	fn = func(data interface{}, root string) error {
		switch data.(type) {
		case string:
			b, cerr := newBundle(data.(string), root)
			if cerr != nil {
				return cerr
			}
			bundles = append(bundles, b)
		case []interface{}:
			for _, v := range data.([]interface{}) {
				if cerr := fn(v, root); cerr != nil {
					return cerr
				}
			}
		case map[string]interface{}:
			for k, v := range data.(map[string]interface{}) {
				r := root
				if len(root) > 0 {
					r += "/"
				}
				if cerr := fn(v, r+k); cerr != nil {
					return cerr
				}
			}
		default:
			return fmt.Errorf("invalid content [%v]", data)
		}
		return nil
	}

	err = fn(data, "")

	return
}

type installedDirSHA struct {
	Dir string
	SHA string
}

func parseInstalled(file string) (i map[string]installedDirSHA, err error) {
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()

	err = json.NewDecoder(f).Decode(&i)
	return
}

func install(bundles []bundle, installed map[string]installedDirSHA) map[string]installedDirSHA {
	q := make(chan struct {
		Name string
		Dir  string
		SHA  string
	})

	go func() {
		defer close(q)

		var wg sync.WaitGroup
		for _, b := range bundles {
			wg.Add(1)

			go func(b bundle) {
				defer wg.Done()

				bundleName := b.Owner + "/" + b.Repo

				branches, err := listBranches(b.Owner, b.Repo)
				if err != nil {
					fmt.Fprintf(os.Stderr, "config error nearby %s\n", bundleName)
					return
				}

				var master branchesResponseCommit
				ok := false
				for _, branch := range branches {
					if branch.Name == "master" {
						master = branch.Commit
						ok = true
						break
					}
				}
				if !ok {
					fmt.Fprintf(os.Stderr, "[%s] master branch not found\n", bundleName)
					return
				}

				src := filepath.Join(cacheDownloadDir, b.Repo+"-"+master.SHA)
				dest := filepath.Join(vimfilesDir, b.Dir, b.Repo)

				dirsha, ok := installed[bundleName]
				if !ok {
					dirsha = installedDirSHA{"", ""}
				} else if _, err = os.Stat(dest); err != nil {
					dirsha = installedDirSHA{"", ""}
				}
				if dirsha.SHA == master.SHA {
					q <- struct {
						Name string
						Dir  string
						SHA  string
					}{bundleName, dirsha.Dir, dirsha.SHA}
					return
				}

				if *list {
					fmt.Println(bundleName)
					return
				}

				archive := filepath.Join(cacheDownloadDir, b.Owner+"_"+b.Repo+".zip")
				if err = download(b.Owner, b.Repo, master.SHA, archive); err != nil {
					fmt.Fprintln(os.Stderr, err)
					return
				}

				if err = unzip(archive, cacheDownloadDir); err != nil {
					fmt.Fprintln(os.Stderr, err)
					return
				}

				if _, err = os.Stat(dest); err != nil {
					if err = os.MkdirAll(dest, 0644); err != nil {
						fmt.Fprintln(os.Stderr, err)
						return
					}
				}

				if _, err = os.Stat(dest); err == nil {
					if err = os.RemoveAll(dest); err != nil {
						fmt.Fprintln(os.Stderr, err)
						return
					}
				}

				if err = os.Rename(src, dest); err != nil {
					fmt.Fprintln(os.Stderr, err)
					return
				}

				q <- struct {
					Name string
					Dir  string
					SHA  string
				}{bundleName, b.Dir, master.SHA}

				fmt.Printf("installed %s\n", bundleName)
				if err = os.Remove(archive); err != nil {
					fmt.Fprintln(os.Stderr, err)
				}
			}(b)
		}

		wg.Wait()
	}()

	newInstalled := make(map[string]installedDirSHA)
	for p := range q {
		newInstalled[p.Name] = installedDirSHA{Dir: p.Dir, SHA: p.SHA}
	}

	return newInstalled
}

func saveInstalled(file string, installed map[string]installedDirSHA) (err error) {
	w, err := os.Create(file)
	if err != nil {
		return
	}
	defer func() {
		err2 := w.Close()
		if err2 != nil {
			err = err2
		}
	}()

	jsonData, err := json.MarshalIndent(installed, "", "  ")
	if err != nil {
		return
	}

	_, err = w.Write(jsonData)
	return
}

func cleanPlugins(bs []bundle, installed map[string]installedDirSHA) (err error) {
	for k, v := range installed {
		rm := true
		for _, b := range bs {
			if k == b.Owner+"/"+b.Repo && v.Dir == b.Dir {
				rm = false
				break
			}
		}

		if rm {
			sp := strings.SplitN(k, "/", 2)
			if len(sp) != 2 {
				err = fmt.Errorf("%s: plugin should be style of :owner/:repo", k)
				return
			}
			// TODO: remove empty directory recursive
			if err = os.RemoveAll(filepath.Join(vimfilesDir, v.Dir, sp[1])); err != nil {
				return
			}

			fmt.Printf("removed %s\n", k)
		}
	}

	return
}

func main() {
	flag.Parse()

	var err error

	*cacheDir, err = homedir.Expand(*cacheDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	vimfilesDir = vimfiles()
	if _, err = os.Stat(vimfilesDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if _, err = os.Stat(*cacheDir); err != nil {
		if err = os.MkdirAll(*cacheDir, 0644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	cacheDownloadDir = filepath.Join(*cacheDir, "archives")
	if _, err = os.Stat(cacheDownloadDir); err != nil {
		if err = os.MkdirAll(cacheDownloadDir, 0644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	cfile := filepath.Join(vimfilesDir, "gardenia.json")
	if _, err = os.Stat(cfile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	bundles, err := parseConfig(cfile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var installed map[string]installedDirSHA
	installedFile := filepath.Join(*cacheDir, "installed.json")
	if *force {
		if _, err = os.Stat(installedFile); err == nil {
			if err = os.Remove(installedFile); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
	if _, err = os.Stat(installedFile); err == nil {
		if installed, err = parseInstalled(installedFile); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	} else {
		installed = make(map[string]installedDirSHA)
	}

	if *clean {
		if err = cleanPlugins(bundles, installed); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	newInstalled := install(bundles, installed)

	if !*list {
		if err = saveInstalled(installedFile, newInstalled); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
