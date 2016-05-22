// Copyright (C) 2016 Nippon Telegraph and Telephone Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"github.com/osrg/gobgp/config"
	"github.com/osrg/gobgp/table"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestModPolicyAssign(t *testing.T) {
	assert := assert.New(t)
	s := NewBgpServer()
	go s.Serve()
	s.SetGlobalType(config.Global{
		Config: config.GlobalConfig{
			As:       1,
			RouterId: "1.1.1.1",
		},
	})
	_, err := s.AddPolicy(&Request{
		Data: &AddPolicyRequest{
			Policy: &Policy{
				Name: "p1",
			},
		},
	})
	assert.Nil(err)
	_, err = s.AddPolicy(&Request{
		Data: &AddPolicyRequest{
			Policy: &Policy{
				Name: "p2",
			},
		},
	})
	assert.Nil(err)
	_, err = s.AddPolicy(&Request{
		Data: &AddPolicyRequest{
			Policy: &Policy{
				Name: "p3",
			},
		},
	})
	assert.Nil(err)
	_, err = s.AddPolicyAssignment(&Request{
		Data: &AddPolicyAssignmentRequest{
			Assignment: &PolicyAssignment{
				Type:     PolicyType_IMPORT,
				Resource: Resource_GLOBAL,
				Policies: []*Policy{&Policy{Name: "p1"}, &Policy{Name: "p2"}, &Policy{Name: "p3"}},
			},
		},
	})
	assert.Nil(err)

	_, err = s.DeletePolicyAssignment(&Request{
		Data: &DeletePolicyAssignmentRequest{
			Assignment: &PolicyAssignment{
				Type:     PolicyType_IMPORT,
				Resource: Resource_GLOBAL,
				Policies: []*Policy{&Policy{Name: "p1"}},
			},
		},
	})
	assert.Nil(err)

	ps := s.policy.GetPolicy(table.GLOBAL_RIB_NAME, table.POLICY_DIRECTION_IMPORT)
	assert.Equal(len(ps), 2)
}
