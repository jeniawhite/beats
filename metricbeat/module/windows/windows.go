// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

//go:build windows

package windows

import (
	"sync"

	"github.com/elastic/beats/v7/metricbeat/helper"
	"github.com/elastic/beats/v7/metricbeat/mb"
	"github.com/elastic/elastic-agent-libs/logp"
)

var once sync.Once

func init() {
	// Register the ModuleFactory function for the "windows" module.
	if err := mb.Registry.AddModule("windows", NewModule); err != nil {
		panic(err)
	}
}

func initModule(logger *logp.Logger) {
	if err := helper.CheckAndEnableSeDebugPrivilege(logger); err != nil {
		logger.Warnf("%v", err)
	}
}

type Module struct {
	mb.BaseModule
}

func NewModule(base mb.BaseModule) (mb.Module, error) {
	once.Do(func() {
		initModule(base.Logger)
	})

	return &Module{BaseModule: base}, nil
}
