// Copyright 2022 The MIDI Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ir

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/moby/buildkit/client/llb"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/tensorchord/MIDI/pkg/flag"
	"github.com/tensorchord/MIDI/pkg/vscode"
)

func NewGraph() *Graph {
	return &Graph{
		OS:       osDefault,
		Language: languageDefault,
		CUDA:     nil,
		CUDNN:    nil,
		BuiltinSystemPackages: []string{
			"curl",
			"openssh-client",
		},

		PyPIPackages:   []string{},
		SystemPackages: []string{},
		Exec:           []llb.State{},
	}
}

var DefaultGraph = NewGraph()

func GPUEnabled() bool {
	return DefaultGraph.CUDA != nil
}

func Compile(ctx context.Context) (*llb.Definition, error) {
	state, err := DefaultGraph.Compile()
	if err != nil {
		return nil, err
	}
	// TODO(gaocegege): Support multi platform.
	def, err := state.Marshal(ctx, llb.LinuxAmd64)
	if err != nil {
		return nil, err
	}
	return def, nil
}

func (g Graph) Compile() (llb.State, error) {
	// TODO(gaocegege): Support more OS and langs.
	base := g.compileBase()
	aptStage := g.compileUbuntuAPT(base)

	builtinSystemStage := g.compileBuiltinSystemPackages(aptStage)
	pypiMirrorStage := g.compilePyPIMirror(builtinSystemStage)
	pypiStage := llb.Diff(aptStage, g.compilePyPIPackages(pypiMirrorStage))

	systemStage := llb.Diff(aptStage, g.compileSystemPackages(aptStage))

	sshStage := g.copyMidiSSHServer()

	vscodeStage, err := g.compileVSCode()
	if err != nil {
		return llb.State{}, errors.Wrap(err, "failed to get vscode plugins")
	}
	if vscodeStage != nil {
		merged := llb.Merge([]llb.State{
			aptStage, systemStage, pypiStage, sshStage, *vscodeStage,
		})
		return merged, nil
	}
	merged := llb.Merge([]llb.State{
		aptStage, systemStage, pypiStage, sshStage,
	})
	return merged, nil
}

func (g *Graph) compileBase() llb.State {
	if g.CUDA == nil && g.CUDNN == nil {
		return llb.Image("docker.io/library/python:3.8")
	}
	return g.compileCUDAPackages()
}

func (g *Graph) compileCUDAPackages() llb.State {
	root := llb.Image(
		fmt.Sprintf("nvidia/cuda:%s.0-cudnn%s-devel-%s", *g.CUDA, *g.CUDNN, g.OS))
	g.BuiltinSystemPackages = append(g.BuiltinSystemPackages, []string{
		g.Language,
		fmt.Sprintf("%s-pip", g.Language),
	}...)
	return root
}

func (g Graph) compilePyPIPackages(root llb.State) llb.State {
	if len(g.PyPIPackages) == 0 {
		return root
	}

	// Compose the package install command.
	var sb strings.Builder
	// TODO(gaocegege): Support per-user config to keep the mirror.
	sb.WriteString("pip install")
	for _, pkg := range g.PyPIPackages {
		sb.WriteString(fmt.Sprintf(" %s", pkg))
	}

	cacheDir := "/root/.cache/pip"

	run := root.Run(llb.Shlex(sb.String()))
	run.AddMount(cacheDir, llb.Scratch(),
		llb.AsPersistentCacheDir("/"+cacheDir, llb.CacheMountShared))
	return run.Root()
}

func (g Graph) compileBuiltinSystemPackages(root llb.State) llb.State {
	if len(g.BuiltinSystemPackages) == 0 {
		return root
	}

	// Compose the package install command.
	var sb strings.Builder
	sb.WriteString(
		"sh -c \"apt-get update && apt-get install -y --no-install-recommends")
	for _, pkg := range g.BuiltinSystemPackages {
		sb.WriteString(fmt.Sprintf(" %s", pkg))
	}
	sb.WriteString("\"")

	cacheDir := "/var/cache/apt"
	cacheLibDir := "/var/lib/apt"

	run := root.Run(llb.Shlex(sb.String()))
	run.AddMount(cacheDir, llb.Scratch(),
		llb.AsPersistentCacheDir("/"+cacheDir, llb.CacheMountShared))
	run.AddMount(cacheLibDir, llb.Scratch(),
		llb.AsPersistentCacheDir("/"+cacheLibDir, llb.CacheMountShared))
	return run.Root()
}

func (g Graph) compileSystemPackages(root llb.State) llb.State {
	if len(g.SystemPackages) == 0 {
		return root
	}

	// Compose the package install command.
	var sb strings.Builder
	// TODO(gaocegege): Support per-user config to keep the mirror.
	sb.WriteString("apt install")

	for _, pkg := range g.SystemPackages {
		sb.WriteString(fmt.Sprintf(" %s", pkg))
	}

	cacheDir := "/var/cache/apt"
	cacheLibDir := "/var/lib/apt"

	run := root.Run(llb.Shlex(sb.String()))
	run.AddMount(cacheDir, llb.Scratch(),
		llb.AsPersistentCacheDir("/"+cacheDir, llb.CacheMountShared))
	run.AddMount(cacheLibDir, llb.Scratch(),
		llb.AsPersistentCacheDir("/"+cacheLibDir, llb.CacheMountShared))
	return run.Root()
}

func (g Graph) copyMidiSSHServer() llb.State {
	// TODO(gaocegege): Remove global var ssh image.
	run := llb.Scratch().
		File(llb.Copy(llb.Image(viper.GetString(flag.FlagSSHImage)),
			"usr/bin/midi-ssh", "/var/midi/bin/midi-ssh",
			&llb.CopyInfo{CreateDestPath: true}))
	return run
}

func (g Graph) compileVSCode() (*llb.State, error) {
	if len(g.VSCodePlugins) == 0 {
		return nil, nil
	}
	inputs := []llb.State{}
	for _, p := range g.VSCodePlugins {
		vscodeClient := vscode.NewClient()
		if err := vscodeClient.DownloadOrCache(p); err != nil {
			return nil, err
		}
		ext := llb.Scratch().File(llb.Copy(llb.Local(flag.FlagCacheDir),
			vscodeClient.PluginPath(p),
			"/root/.vscode-server/extensions/"+p.String(),
			&llb.CopyInfo{CreateDestPath: true}))
		inputs = append(inputs, ext)
	}
	layer := llb.Merge(inputs)
	return &layer, nil
}

func (g Graph) compileUbuntuAPT(root llb.State) llb.State {
	if g.UbuntuAPTSource != nil {
		logrus.WithField("source", *g.UbuntuAPTSource).Debug("using custom APT source")
		aptSource := llb.Scratch().
			File(llb.Mkdir(filepath.Dir(aptSourceFilePath), 0755, llb.WithParents(true))).
			File(llb.Mkfile(aptSourceFilePath, 0644, []byte(*g.UbuntuAPTSource)))
		return llb.Merge([]llb.State{root, aptSource})
	}
	return root
}

func (g Graph) compilePyPIMirror(root llb.State) llb.State {
	if g.PyPIMirror != nil {
		logrus.WithField("mirror", *g.PyPIMirror).Debug("using custom PyPI mirror")
		content := fmt.Sprintf(pypiConfigTemplate, *g.PyPIMirror)
		aptSource := llb.Scratch().
			File(llb.Mkdir(filepath.Dir(pypiMirrorFilePath), 0755, llb.WithParents(true))).
			File(llb.Mkfile(pypiMirrorFilePath, 0644, []byte(content)))
		return llb.Merge([]llb.State{root, aptSource})
	}
	return root
}