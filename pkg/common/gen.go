// Copyright 2023 Ant Group Co., Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package common

import (
	"regexp"

	"github.com/google/uuid"
)

func GenDomainDataID(dataName string) (dataID string) {
	// reserve the valid characters in the string
	reg, _ := regexp.Compile("[^a-zA-Z0-9/-]+")
	s1 := reg.ReplaceAllString(dataName, "")
	// remove the invalid characters ['0-9' and '-'] at the begin of the string
	reg, _ = regexp.Compile("^[0-9/-]+")
	prefix := reg.ReplaceAllString(s1, "")
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	return prefix + uuid.NewString()
}