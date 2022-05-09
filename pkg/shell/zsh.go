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

package shell

import (
	_ "embed"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/tensorchord/MIDI/pkg/home"
	"github.com/tensorchord/MIDI/pkg/util/fileutil"
)

//go:embed install.sh
var installScript string

type Manager interface {
	InstallScript() string
	DownloadOrCache() error
	OHMyZSHDir() string
}

type generalManager struct {
}

func NewManager() Manager {
	return &generalManager{}
}

func (m generalManager) InstallScript() string {
	return installScript
}

func (m generalManager) DownloadOrCache() error {
	if ok, err := fileutil.DirExists(m.OHMyZSHDir()); err != nil {
		return err
	} else if ok {
		return nil
	}
	_, err := git.PlainClone(m.OHMyZSHDir(), false, &git.CloneOptions{
		URL: "https://github.com/ohmyzsh/ohmyzsh",
	})
	if err != nil {
		return err
	}

	return nil
}

func (m generalManager) OHMyZSHDir() string {
	return filepath.Join(home.GetManager().CacheDir(), "oh-my-zsh")
}