///bin/true ; exec /usr/bin/env go run "$0" "$@"
// Copyright 2017 The Fuchsia Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

var archive = flag.Bool("archive", true, "Whether to archive the output")
var output = flag.String("output", "fuchsia-sdk.tgz", "Name of the archive")
var outDir = flag.String("out-dir", "", "Output directory")
var toolchain = flag.Bool("toolchain", false, "Include toolchain")
var toolchainLibs = flag.Bool("toolchain-lib", true, "Include toolchain libraries in SDK. Typically used when --toolchain is false")
var sysroot = flag.Bool("sysroot", true, "Include sysroot")
var kernelImg = flag.Bool("kernel-img", true, "Include kernel image")
var kernelDebugObjs = flag.Bool("kernel-dbg", true, "Include kernel objects with debug symbols")
var bootdata = flag.Bool("bootdata", true, "Include bootdata")
var qemu = flag.Bool("qemu", true, "Include QEMU binary")
var tools = flag.Bool("tools", true, "Include additional tools")
var media = flag.Bool("media", true, "Include C media library")
var verbose = flag.Bool("v", false, "Verbose output")
var dryRun = flag.Bool("n", false, "Dry run - print what would happen but don't actually do it")

type compType int

const (
	dirType compType = iota
	fileType
	customType
)

const x64BuildDir = "out/release-x64"
const armBuildDir = "out/release-arm64"

type component struct {
	flag      *bool  // Flag controlling whether this component should be included
	srcPrefix string // Source path prefix relative to the fuchsia root
	dstPrefix string // Destination path prefix relative to the SDK root
	t         compType
	f         func(src, dst string) // When t is 'custom', function to run to copy
}

type dir struct {
	flag     *bool
	src, dst string
}

type file struct {
	flag     *bool
	src, dst string
}

type clientHeader struct {
	flag *bool
	src  string // Path to the source of the header, relative to root of the Fuchsia tree
	dst  string // Path within the target sysroot
}

type clientLib struct {
	flag *bool
	name string
}

var (
	hostOs     string
	hostCpu    string
	components []component
)

func init() {
	hostCpu = runtime.GOARCH
	if hostCpu == "amd64" {
		hostCpu = "x64"
	}
	hostOs = runtime.GOOS
	if hostOs == "darwin" {
		hostOs = "mac"
	}

	zxBuildDir := "out/build-zircon"
	x64ZxBuildDir := path.Join(zxBuildDir, "build-x64")
	armZxBuildDir := path.Join(zxBuildDir, "build-arm64")
	x64BuildBootfsDir := "out/release-x64-bootfs/"
	armBuildBootfsDir := "out/release-arm64-bootfs/"
	qemuDir := fmt.Sprintf("buildtools/%s-%s/qemu/", hostOs, hostCpu)

	dirs := []dir{
		{
			sysroot,
			path.Join(x64BuildDir, "sdks/zircon_sysroot/sysroot/"),
			"sysroot/x86_64-fuchsia/",
		},
		{
			sysroot,
			path.Join(armBuildDir, "sdks/zircon_sysroot/sysroot/"),
			"sysroot/aarch64-fuchsia",
		},
		{
			qemu,
			qemuDir,
			"qemu",
		},
		{
			tools,
			"out/build-zircon/tools",
			"tools",
		},
		{
			tools,
			path.Join(x64BuildDir, "host_x64/far"),
			"tools/far",
		},
		{
			tools,
			path.Join(x64BuildDir, "host_x64/pm"),
			"tools/pm",
		},
		{
			toolchain,
			fmt.Sprintf("buildtools/%s-%s/clang", hostOs, hostCpu),
			"clang",
		},
		{
			// TODO(https://crbug.com/724204): Remove this once Chromium starts using upstream compiler-rt builtins.
			toolchainLibs,
			fmt.Sprintf("buildtools/%s-%s/clang/lib/clang/7.0.0/lib/fuchsia", hostOs, hostCpu),
			"toolchain_libs/clang/7.0.0/lib/fuchsia",
		},
	}

	clientHeaders := []clientHeader{
		{
			media,
			"garnet/public/lib/media/c/audio.h",
			"media/audio.h",
		},
		{
			sysroot,
			"garnet/public/lib/netstack/c/netconfig.h",
			"netstack/netconfig.h",
		},
	}

	clientLibs := []clientLib{
		{
			media,
			"libmedia_client.so",
		},
	}

	files := []file{
		{
			kernelImg,
			"out/build-zircon/build-arm64/qemu-zircon.bin",
			"target/aarch64/zircon.bin",
		},
		{
			kernelImg,
			path.Join(armBuildDir, "bootdata-blob-qemu.bin"),
			"target/aarch64/bootdata-blob.bin",
		},
		{
			kernelImg,
			path.Join(armBuildDir, "images/fvm.blk"),
			"target/aarch64/fvm.blk",
		},

		{
			kernelImg,
			"out/build-zircon/build-x64/zircon.bin",
			"target/x86_64/zircon.bin",
		},
		{
			kernelImg,
			path.Join(x64BuildDir, "bootdata-blob-pc.bin"),
			"target/x86_64/bootdata-blob.bin",
		},
		{
			kernelImg,
			path.Join(x64BuildDir, "images/local-pc.esp.blk"),
			"target/x86_64/local.esp.blk",
		},
		{
			kernelImg,
			path.Join(x64BuildDir, "images/zircon-pc.vboot"),
			"target/x86_64/zircon.vboot",
		},
		{
			kernelImg,
			path.Join(x64BuildDir, "images/fvm.blk"),
			"target/x86_64/fvm.blk",
		},
		{
			kernelImg,
			path.Join(x64BuildDir, "images/fvm.sparse.blk"),
			"target/x86_64/fvm.sparse.blk",
		},

		// TODO(marshallk): Remove this when bootfs is deprecated.
		{
			bootdata,
			path.Join(x64BuildBootfsDir, "user.bootfs"),
			"target/x86_64/bootdata.bin",
		},
		{
			bootdata,
			path.Join(armBuildBootfsDir, "user.bootfs"),
			"target/aarch64/bootdata.bin",
		},
	}

	components = []component{
		{
			kernelDebugObjs,
			x64ZxBuildDir,
			"sysroot/x86_64-fuchsia/debug",
			customType,
			copyKernelDebugObjs,
		},
		{
			kernelDebugObjs,
			x64BuildDir,
			"sysroot/x86_64-fuchsia/debug",
			customType,
			copyIdsTxt,
		},
		{
			kernelDebugObjs,
			armZxBuildDir,
			"sysroot/aarch64-fuchsia/debug",
			customType,
			copyKernelDebugObjs,
		},
		{
			kernelDebugObjs,
			armBuildDir,
			"sysroot/aarch64-fuchsia/debug",
			customType,
			copyIdsTxt,
		},
	}
	for _, c := range clientHeaders {
		files = append(files, file{c.flag, c.src, path.Join("sysroot/x86_64-fuchsia/include", c.dst)})
		files = append(files, file{c.flag, c.src, path.Join("sysroot/aarch64-fuchsia/include", c.dst)})
	}
	for _, c := range clientLibs {
		files = append(files, file{c.flag, path.Join(x64BuildDir, "x64-shared", c.name), path.Join("sysroot/x86_64-fuchsia/lib", c.name)})
		files = append(files, file{c.flag, path.Join(x64BuildDir, "x64-shared/lib.unstripped", c.name), path.Join("sysroot/x86_64-fuchsia/debug", c.name)})
		files = append(files, file{c.flag, path.Join(armBuildDir, "arm64-shared", c.name), path.Join("sysroot/aarch64-fuchsia/lib", c.name)})
		files = append(files, file{c.flag, path.Join(armBuildDir, "arm64-shared/lib.unstripped", c.name), path.Join("sysroot/aarch64-fuchsia/debug", c.name)})
	}
	for _, d := range dirs {
		components = append(components, component{d.flag, d.src, d.dst, dirType, nil})
	}
	for _, f := range files {
		components = append(components, component{f.flag, f.src, f.dst, fileType, nil})
	}
}

func createLayout(manifest, fuchsiaRoot, outDir string) {
	for idx, buildDir := range []string{x64BuildDir, armBuildDir} {
		manifestPath := filepath.Join(fuchsiaRoot, buildDir, "sdk-manifests", manifest)
		cmd := filepath.Join(fuchsiaRoot, "scripts", "sdk", "create_layout.py")
		args := []string{"--manifest", manifestPath, "--output", outDir}
		if idx > 0 {
			args = append(args, "--overlay")
		}
		if *verbose || *dryRun {
			fmt.Println("createLayout", cmd, args)
		}
		if *dryRun {
			return
		}
		out, err := exec.Command(cmd, args...).Output()
		if err != nil {
			log.Fatal("create_layout.py failed with output", string(out), "error", err)
		}
	}
}

func copyKernelDebugObjs(src, dstPrefix string) {
	// The kernel debug information lives in many .elf files in the out directory
	filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() && filepath.Ext(path) == ".elf" {
			dst := filepath.Join(dstPrefix, path[len(src):])
			mkdir(filepath.Dir(dst))
			cp(path, dst)
		}
		return nil
	})
}

func copyIdsTxt(src, dstPrefix string) {
	// The ids.txt file has absolute paths but relative paths within the SDK are
	// more useful to users.
	srcIds, err := os.Open(filepath.Join(src, "ids.txt"))
	if err != nil {
		log.Fatal("could not open ids.txt", err)
	}
	defer srcIds.Close()
	if *dryRun {
		return
	}
	dstIds, err := os.Create(filepath.Join(dstPrefix, "ids.txt"))
	if err != nil {
		log.Fatal("could not create ids.txt", err)
	}
	defer dstIds.Close()
	scanner := bufio.NewScanner(srcIds)
	cwd, _ := os.Getwd()
	absBase := filepath.Join(cwd, src)
	for scanner.Scan() {
		s := strings.Split(scanner.Text(), " ")
		id, absPath := s[0], s[1]
		relPath, err := filepath.Rel(absBase, absPath)
		if err != nil {
			log.Println("could not create relative path from absolute path", absPath, "and base", absBase, "skipping entry")
		} else {
			fmt.Fprintln(dstIds, id, relPath)
		}
	}
}

func mkdir(d string) {
	if *verbose || *dryRun {
		fmt.Println("Making directory", d)
	}
	if *dryRun {
		return
	}
	_, err := exec.Command("mkdir", "-p", d).Output()
	if err != nil {
		log.Fatal("could not create directory", d)
	}
}

func cp(args ...string) {
	if *verbose || *dryRun {
		fmt.Println("Copying", args)
	}
	if *dryRun {
		return
	}
	out, err := exec.Command("cp", args...).CombinedOutput()
	if err != nil {
		log.Fatal("cp failed with output ", string(out), " error ", err, args)
	}
}

func copyFile(src, dst string) {
	mkdir(filepath.Dir(dst))
	cp(src, dst)
}

func copyDir(src, dst string) {
	mkdir(filepath.Dir(dst))
	cp("-r", src, "-T", dst)
}

func tar(src, dst string) {
	if *verbose || *dryRun {
		fmt.Println("Archiving", src, "to", dst)
	}
	if *dryRun {
		return
	}
	out, err := exec.Command("tar", "cvzf", dst, "-C", src, ".").Output()
	if err != nil {
		log.Fatal("tar failed with output", string(out), "error", err)
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: ./makesdk.go [flags] /path/to/fuchsia/root

This script creates a Fuchsia SDK containing the specified features and places it into a tarball.

To use, first build a release mode Fuchsia build with the 'runtime' module configured as the
only module.
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	fuchsiaRoot := flag.Arg(0)
	if _, err := os.Stat(fuchsiaRoot); os.IsNotExist(err) {
		flag.Usage()
		log.Fatalf("Fuchsia root not found at \"%v\"\n", fuchsiaRoot)
	}
	if *outDir == "" {
		var err error
		*outDir, err = ioutil.TempDir("", "fuchsia-sdk")
		if err != nil {
			log.Fatal("Could not create temporary directory: ", err)
		}
		defer os.RemoveAll(*outDir)
	} else if _, err := os.Stat(fuchsiaRoot); os.IsNotExist(err) {
		mkdir(filepath.Dir(*outDir))
	}

	createLayout("garnet", fuchsiaRoot, *outDir)

	for _, c := range components {
		if *c.flag {
			src := filepath.Join(fuchsiaRoot, c.srcPrefix)
			dst := filepath.Join(*outDir, c.dstPrefix)
			switch c.t {
			case dirType:
				copyDir(src, dst)
			case fileType:
				copyFile(src, dst)
			case customType:
				c.f(src, dst)
			}
		}
	}
	if *archive {
		tar(*outDir, *output)
	}
}
