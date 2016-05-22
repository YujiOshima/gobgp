// Copyright (C) 2014 Nippon Telegraph and Telephone Corporation.
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

import fmt "fmt"
import math "math"

const (
	_ = iota
	REQ_GET_SERVER
	REQ_START_SERVER
	REQ_STOP_SERVER
	REQ_NEIGHBOR
	REQ_ADJ_RIB_IN
	REQ_ADJ_RIB_OUT
	REQ_LOCAL_RIB
	REQ_NEIGHBOR_RESET
	REQ_NEIGHBOR_SOFT_RESET
	REQ_NEIGHBOR_SOFT_RESET_IN
	REQ_NEIGHBOR_SOFT_RESET_OUT
	REQ_NEIGHBOR_SHUTDOWN
	REQ_NEIGHBOR_ENABLE
	REQ_NEIGHBOR_DISABLE
	REQ_ADD_NEIGHBOR
	REQ_DEL_NEIGHBOR
	// FIXME: we should merge
	REQ_GRPC_ADD_NEIGHBOR
	REQ_GRPC_DELETE_NEIGHBOR
	REQ_UPDATE_NEIGHBOR
	REQ_GLOBAL_RIB
	REQ_MONITOR_GLOBAL_BEST_CHANGED
	REQ_MONITOR_INCOMING
	REQ_MONITOR_NEIGHBOR_PEER_STATE
	REQ_ENABLE_MRT
	REQ_DISABLE_MRT
	REQ_INJECT_MRT
	REQ_ADD_BMP
	REQ_DELETE_BMP
	REQ_VALIDATE_RIB
	// TODO: delete
	REQ_INITIALIZE_RPKI
	REQ_GET_RPKI
	REQ_ADD_RPKI
	REQ_DELETE_RPKI
	REQ_ENABLE_RPKI
	REQ_DISABLE_RPKI
	REQ_RESET_RPKI
	REQ_SOFT_RESET_RPKI
	REQ_ROA
	REQ_ADD_VRF
	REQ_DELETE_VRF
	REQ_VRF
	REQ_GET_VRF
	REQ_ADD_PATH
	REQ_DELETE_PATH
	REQ_GET_DEFINED_SET
	REQ_ADD_DEFINED_SET
	REQ_DELETE_DEFINED_SET
	REQ_REPLACE_DEFINED_SET
	REQ_GET_STATEMENT
	REQ_ADD_STATEMENT
	REQ_DELETE_STATEMENT
	REQ_REPLACE_STATEMENT
	REQ_GET_POLICY
	REQ_ADD_POLICY
	REQ_DELETE_POLICY
	REQ_REPLACE_POLICY
	REQ_GET_POLICY_ASSIGNMENT
	REQ_ADD_POLICY_ASSIGNMENT
	REQ_DELETE_POLICY_ASSIGNMENT
	REQ_REPLACE_POLICY_ASSIGNMENT
	REQ_BMP_NEIGHBORS
	REQ_BMP_GLOBAL
	REQ_BMP_ADJ_IN
	REQ_DEFERRAL_TIMER_EXPIRED
	REQ_RELOAD_POLICY
	REQ_INITIALIZE_ZEBRA
)

type Request struct {
	RequestType int
	Name        string
	RouteFamily bgp.RouteFamily
	ResponseCh  chan *GrpcResponse
	EndCh       chan struct{}
	Err         error
	Data        interface{}
}

func NewRequest(name string, rf bgp.RouteFamily, d interface{}) *GrpcRequest {
	r := &Request{
		RouteFamily: rf,
		Name:        name,
		ResponseCh:  make(chan *Response, 8),
		EndCh:       make(chan struct{}, 1),
		Data:        d,
	}
	return r
}

type Response struct {
	ResponseErr error
	Data        interface{}
}

func (r *Response) Err() error {
	return r.ResponseErr
}

type Resource int32

const (
	Resource_GLOBAL  Resource = 0
	Resource_LOCAL   Resource = 1
	Resource_ADJ_IN  Resource = 2
	Resource_ADJ_OUT Resource = 3
	Resource_VRF     Resource = 4
)

var Resource_name = map[int32]string{
	0: "GLOBAL",
	1: "LOCAL",
	2: "ADJ_IN",
	3: "ADJ_OUT",
	4: "VRF",
}
var Resource_value = map[string]int32{
	"GLOBAL":  0,
	"LOCAL":   1,
	"ADJ_IN":  2,
	"ADJ_OUT": 3,
	"VRF":     4,
}

type DefinedType int32

const (
	DefinedType_PREFIX        DefinedType = 0
	DefinedType_NEIGHBOR      DefinedType = 1
	DefinedType_TAG           DefinedType = 2
	DefinedType_AS_PATH       DefinedType = 3
	DefinedType_COMMUNITY     DefinedType = 4
	DefinedType_EXT_COMMUNITY DefinedType = 5
)

var DefinedType_name = map[int32]string{
	0: "PREFIX",
	1: "NEIGHBOR",
	2: "TAG",
	3: "AS_PATH",
	4: "COMMUNITY",
	5: "EXT_COMMUNITY",
}
var DefinedType_value = map[string]int32{
	"PREFIX":        0,
	"NEIGHBOR":      1,
	"TAG":           2,
	"AS_PATH":       3,
	"COMMUNITY":     4,
	"EXT_COMMUNITY": 5,
}

type MatchType int32

const (
	MatchType_ANY    MatchType = 0
	MatchType_ALL    MatchType = 1
	MatchType_INVERT MatchType = 2
)

var MatchType_name = map[int32]string{
	0: "ANY",
	1: "ALL",
	2: "INVERT",
}
var MatchType_value = map[string]int32{
	"ANY":    0,
	"ALL":    1,
	"INVERT": 2,
}

type AsPathLengthType int32

const (
	AsPathLengthType_EQ AsPathLengthType = 0
	AsPathLengthType_GE AsPathLengthType = 1
	AsPathLengthType_LE AsPathLengthType = 2
)

var AsPathLengthType_name = map[int32]string{
	0: "EQ",
	1: "GE",
	2: "LE",
}
var AsPathLengthType_value = map[string]int32{
	"EQ": 0,
	"GE": 1,
	"LE": 2,
}

type RouteAction int32

const (
	RouteAction_NONE   RouteAction = 0
	RouteAction_ACCEPT RouteAction = 1
	RouteAction_REJECT RouteAction = 2
)

var RouteAction_name = map[int32]string{
	0: "NONE",
	1: "ACCEPT",
	2: "REJECT",
}
var RouteAction_value = map[string]int32{
	"NONE":   0,
	"ACCEPT": 1,
	"REJECT": 2,
}

type CommunityActionType int32

const (
	CommunityActionType_COMMUNITY_ADD     CommunityActionType = 0
	CommunityActionType_COMMUNITY_REMOVE  CommunityActionType = 1
	CommunityActionType_COMMUNITY_REPLACE CommunityActionType = 2
)

var CommunityActionType_name = map[int32]string{
	0: "COMMUNITY_ADD",
	1: "COMMUNITY_REMOVE",
	2: "COMMUNITY_REPLACE",
}
var CommunityActionType_value = map[string]int32{
	"COMMUNITY_ADD":     0,
	"COMMUNITY_REMOVE":  1,
	"COMMUNITY_REPLACE": 2,
}

type MedActionType int32

const (
	MedActionType_MED_MOD     MedActionType = 0
	MedActionType_MED_REPLACE MedActionType = 1
)

var MedActionType_name = map[int32]string{
	0: "MED_MOD",
	1: "MED_REPLACE",
}
var MedActionType_value = map[string]int32{
	"MED_MOD":     0,
	"MED_REPLACE": 1,
}

type PolicyType int32

const (
	PolicyType_IN     PolicyType = 0
	PolicyType_IMPORT PolicyType = 1
	PolicyType_EXPORT PolicyType = 2
)

var PolicyType_name = map[int32]string{
	0: "IN",
	1: "IMPORT",
	2: "EXPORT",
}
var PolicyType_value = map[string]int32{
	"IN":     0,
	"IMPORT": 1,
	"EXPORT": 2,
}

type SoftResetNeighborRequest_SoftResetDirection int32

const (
	SoftResetNeighborRequest_IN   SoftResetNeighborRequest_SoftResetDirection = 0
	SoftResetNeighborRequest_OUT  SoftResetNeighborRequest_SoftResetDirection = 1
	SoftResetNeighborRequest_BOTH SoftResetNeighborRequest_SoftResetDirection = 2
)

var SoftResetNeighborRequest_SoftResetDirection_name = map[int32]string{
	0: "IN",
	1: "OUT",
	2: "BOTH",
}
var SoftResetNeighborRequest_SoftResetDirection_value = map[string]int32{
	"IN":   0,
	"OUT":  1,
	"BOTH": 2,
}

type AddBmpRequest_MonitoringPolicy int32

const (
	AddBmpRequest_PRE  AddBmpRequest_MonitoringPolicy = 0
	AddBmpRequest_POST AddBmpRequest_MonitoringPolicy = 1
	AddBmpRequest_BOTH AddBmpRequest_MonitoringPolicy = 2
)

var AddBmpRequest_MonitoringPolicy_name = map[int32]string{
	0: "PRE",
	1: "POST",
	2: "BOTH",
}
var AddBmpRequest_MonitoringPolicy_value = map[string]int32{
	"PRE":  0,
	"POST": 1,
	"BOTH": 2,
}

type GetNeighborRequest struct {
}

type GetNeighborResponse struct {
	Peers []*config.Neighbor
}

type Arguments struct {
	Resource Resource
	Family   uint32
	Name     string
}

type AddPathRequest struct {
	Resource Resource
	VrfId    string
	Path     *Path
}

type AddPathResponse struct {
	Uuid []byte
}

type DeletePathRequest struct {
	Resource Resource
	VrfId    string
	Family   uint32
	Path     *Path
	Uuid     []byte
}

type DeletePathResponse struct {
}

type AddNeighborRequest struct {
	Peer *config.Neighbor
}

type AddNeighborResponse struct {
}

type DeleteNeighborRequest struct {
	Peer *config.Neighbor
}

type DeleteNeighborResponse struct {
}

type ResetNeighborRequest struct {
	Address string
}

type ResetNeighborResponse struct {
}

type SoftResetNeighborRequest struct {
	Address   string
	Direction SoftResetNeighborRequest_SoftResetDirection
}

type SoftResetNeighborResponse struct {
}

type ShutdownNeighborRequest struct {
	Address string
}

type ShutdownNeighborResponse struct {
}

type EnableNeighborRequest struct {
	Address string
}

type EnableNeighborResponse struct {
}

type DisableNeighborRequest struct {
	Address string
}

type DisableNeighborResponse struct {
}

type EnableMrtRequest struct {
	DumpType int32
	Filename string
	Interval uint64
}

type EnableMrtResponse struct {
}

type DisableMrtRequest struct {
}

type DisableMrtResponse struct {
}

type InjectMrtRequest struct {
	Resource Resource
	VrfId    string
	Paths    []*Path
}

type InjectMrtResponse struct {
}

type AddBmpRequest struct {
	Address string
	Port    uint32
	Type    AddBmpRequest_MonitoringPolicy
}

type AddBmpResponse struct {
}

type DeleteBmpRequest struct {
	Address string
	Port    uint32
}

type DeleteBmpResponse struct {
}

type RPKIConf struct {
	Address    string
	RemotePort string
}

type RPKIState struct {
	Uptime        int64
	Downtime      int64
	Up            bool
	RecordIpv4    uint32
	RecordIpv6    uint32
	PrefixIpv4    uint32
	PrefixIpv6    uint32
	Serial        uint32
	ReceivedIpv4  int64
	ReceivedIpv6  int64
	SerialNotify  int64
	CacheReset    int64
	CacheResponse int64
	EndOfData     int64
	Error         int64
	SerialQuery   int64
	ResetQuery    int64
}

type Rpki struct {
	Conf  *RPKIConf
	State *RPKIState
}

type GetRpkiRequest struct {
	Family uint32
}

type GetRpkiResponse struct {
	Servers []*Rpki
}

type AddRpkiRequest struct {
	Address  string
	Port     uint32
	Lifetime int64
}

type AddRpkiResponse struct {
}

type DeleteRpkiRequest struct {
	Address string
	Port    uint32
}

type DeleteRpkiResponse struct {
}

type EnableRpkiRequest struct {
	Address string
}

type EnableRpkiResponse struct {
}

type DisableRpkiRequest struct {
	Address string
}

type DisableRpkiResponse struct {
}

type ResetRpkiRequest struct {
	Address string
}

type ResetRpkiResponse struct {
}

type SoftResetRpkiRequest struct {
	Address string
}

type SoftResetRpkiResponse struct {
}

type GetVrfRequest struct {
}

type GetVrfResponse struct {
	Vrfs []*table.Vrf
}

type AddVrfRequest struct {
	Vrf *table.Vrf
}

type AddVrfResponse struct {
}

type DeleteVrfRequest struct {
	Vrf *table.Vrf
}

type DeleteVrfResponse struct {
}

type GetDefinedSetRequest struct {
	Type DefinedType
}

type GetDefinedSetResponse struct {
	Sets []*DefinedSet
}

type AddDefinedSetRequest struct {
	Set *DefinedSet
}

type AddDefinedSetResponse struct {
}

type DeleteDefinedSetRequest struct {
	Set *DefinedSet
	All bool
}

type DeleteDefinedSetResponse struct {
}

type ReplaceDefinedSetRequest struct {
	Set *DefinedSet
}

type ReplaceDefinedSetResponse struct {
}

type GetStatementRequest struct {
}

type GetStatementResponse struct {
	Statements []*Statement
}

type AddStatementRequest struct {
	Statement *Statement
}

type AddStatementResponse struct {
}

type DeleteStatementRequest struct {
	Statement *Statement
	All       bool
}

type DeleteStatementResponse struct {
}

type ReplaceStatementRequest struct {
	Statement *Statement
}

type ReplaceStatementResponse struct {
}

type GetPolicyRequest struct {
}

type GetPolicyResponse struct {
	Policies []*Policy
}

type AddPolicyRequest struct {
	Policy *Policy
	// if this flag is set, gobgpd won't define new statements
	// but refer existing statements using statement's names in this arguments.
	ReferExistingStatements bool
}

type AddPolicyResponse struct {
}

type DeletePolicyRequest struct {
	Policy *Policy
	// if this flag is set, gobgpd won't delete any statements
	// even if some statements get not used by any policy by this operation.
	PreserveStatements bool
	All                bool
}

type DeletePolicyResponse struct {
}

type ReplacePolicyRequest struct {
	Policy *Policy
	// if this flag is set, gobgpd won't define new statements
	// but refer existing statements using statement's names in this arguments.
	ReferExistingStatements bool
	// if this flag is set, gobgpd won't delete any statements
	// even if some statements get not used by any policy by this operation.
	PreserveStatements bool
}

type ReplacePolicyResponse struct {
}

type GetPolicyAssignmentRequest struct {
	Assignment *PolicyAssignment
}

type GetPolicyAssignmentResponse struct {
	Assignment *PolicyAssignment
}

type AddPolicyAssignmentRequest struct {
	Assignment *PolicyAssignment
}

type AddPolicyAssignmentResponse struct {
}

type DeletePolicyAssignmentRequest struct {
	Assignment *PolicyAssignment
	All        bool
}

type DeletePolicyAssignmentResponse struct {
}

type ReplacePolicyAssignmentRequest struct {
	Assignment *PolicyAssignment
}

type ReplacePolicyAssignmentResponse struct {
}

type GetServerRequest struct {
}

type GetServerResponse struct {
	Global *Global
}

type StartServerRequest struct {
	Global *Global
}

type StartServerResponse struct {
}

type StopServerRequest struct {
}

type StopServerResponse struct {
}

type Path struct {
	Nlri               []byte
	Pattrs             [][]byte
	Age                int64
	Best               bool
	IsWithdraw         bool
	Validation         int32
	NoImplicitWithdraw bool
	Family             uint32
	SourceAsn          uint32
	SourceId           string
	Filtered           bool
	Stale              bool
	IsFromExternal     bool
	NeighborIp         string
}

type Destination struct {
	Prefix         string
	Paths          []*Path
	LongerPrefixes bool
}

type Table struct {
	Type         Resource
	Name         string
	Family       uint32
	Destinations []*Destination
	PostPolicy   bool
}

type GetRibRequest struct {
	Table *Table
}

type GetRibResponse struct {
	Table *Table
}

type ValidateRibRequest struct {
	Type   Resource
	Family uint32
	Prefix string
}

type ValidateRibResponse struct {
}

type RouteServer struct {
	RouteServerClient bool
}

type Prefix struct {
	IpPrefix      string
	MaskLengthMin uint32
	MaskLengthMax uint32
}

type DefinedSet struct {
	Type     DefinedType
	Name     string
	List     []string
	Prefixes []*Prefix
}

type MatchSet struct {
	Type MatchType
	Name string
}

type AsPathLength struct {
	Type   AsPathLengthType
	Length uint32
}

type Conditions struct {
	PrefixSet       *MatchSet
	NeighborSet     *MatchSet
	AsPathLength    *AsPathLength
	AsPathSet       *MatchSet
	CommunitySet    *MatchSet
	ExtCommunitySet *MatchSet
	RpkiResult      int32
}

type CommunityAction struct {
	Type        CommunityActionType
	Communities []string
}

type MedAction struct {
	Type  MedActionType
	Value int64
}

type AsPrependAction struct {
	Asn         uint32
	Repeat      uint32
	UseLeftMost bool
}

type NexthopAction struct {
	Address string
}

type Actions struct {
	RouteAction  RouteAction
	Community    *CommunityAction
	Med          *MedAction
	AsPrepend    *AsPrependAction
	ExtCommunity *CommunityAction
	Nexthop      *NexthopAction
}

type Statement struct {
	Name       string
	Conditions *Conditions
	Actions    *Actions
}

type Policy struct {
	Name       string
	Statements []*Statement
}

type PolicyAssignment struct {
	Type     PolicyType
	Resource Resource
	Name     string
	Policies []*Policy
	Default  RouteAction
}

type Roa struct {
	As        uint32
	Prefixlen uint32
	Maxlen    uint32
	Prefix    string
	Conf      *RPKIConf
}

type GetRoaRequest struct {
	Family uint32
}

type GetRoaResponse struct {
	Roas []*Roa
}

type Global struct {
	As              uint32
	RouterId        string
	ListenPort      int32
	ListenAddresses []string
	Families        []uint32
	MplsLabelMin    uint32
	MplsLabelMax    uint32
}
