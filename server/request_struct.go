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

type ApiResource int32

const (
	Resource_GLOBAL  ApiResource = 0
	Resource_LOCAL   ApiResource = 1
	Resource_ADJ_IN  ApiResource = 2
	Resource_ADJ_OUT ApiResource = 3
	Resource_VRF     ApiResource = 4
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

type ApiDefinedType int32

const (
	DefinedType_PREFIX        ApiDefinedType = 0
	DefinedType_NEIGHBOR      ApiDefinedType = 1
	DefinedType_TAG           ApiDefinedType = 2
	DefinedType_AS_PATH       ApiDefinedType = 3
	DefinedType_COMMUNITY     ApiDefinedType = 4
	DefinedType_EXT_COMMUNITY ApiDefinedType = 5
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

type ApiMatchType int32

const (
	MatchType_ANY    ApiMatchType = 0
	MatchType_ALL    ApiMatchType = 1
	MatchType_INVERT ApiMatchType = 2
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

type ApiAsPathLengthType int32

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

type ApiRouteAction int32

const (
	RouteAction_NONE   ApiRouteAction = 0
	RouteAction_ACCEPT ApiRouteAction = 1
	RouteAction_REJECT ApiRouteAction = 2
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

type ApiCommunityActionType int32

const (
	CommunityActionType_COMMUNITY_ADD     ApiCommunityActionType = 0
	CommunityActionType_COMMUNITY_REMOVE  ApiCommunityActionType = 1
	CommunityActionType_COMMUNITY_REPLACE ApiCommunityActionType = 2
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

type ApiMedActionType int32

const (
	MedActionType_MED_MOD     ApiMedActionType = 0
	MedActionType_MED_REPLACE ApiMedActionType = 1
)

var MedActionType_name = map[int32]string{
	0: "MED_MOD",
	1: "MED_REPLACE",
}
var MedActionType_value = map[string]int32{
	"MED_MOD":     0,
	"MED_REPLACE": 1,
}

type ApiPolicyType int32

const (
	PolicyType_IN     ApiPolicyType = 0
	PolicyType_IMPORT ApiPolicyType = 1
	PolicyType_EXPORT ApiPolicyType = 2
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

type ApiSoftResetNeighborRequest_SoftResetDirection int32

const (
	SoftResetNeighborRequest_IN   ApiSoftResetNeighborRequest_SoftResetDirection = 0
	SoftResetNeighborRequest_OUT  ApiSoftResetNeighborRequest_SoftResetDirection = 1
	SoftResetNeighborRequest_BOTH ApiSoftResetNeighborRequest_SoftResetDirection = 2
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

type ApiAddBmpRequest_MonitoringPolicy int32

const (
	AddBmpRequest_PRE  ApiAddBmpRequest_MonitoringPolicy = 0
	AddBmpRequest_POST ApiAddBmpRequest_MonitoringPolicy = 1
	AddBmpRequest_BOTH ApiAddBmpRequest_MonitoringPolicy = 2
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
	Peers []*Peer
}

type Arguments struct {
	Resource ApiResource
	Family   uint32
	Name     string
}

type AddPathRequest struct {
	Resource ApiResource
	VrfId    string
	Path     *Path
}

type AddPathResponse struct {
	Uuid []byte
}

type DeletePathRequest struct {
	Resource ApiApiResource
	VrfId    string
	Family   uint32
	Path     *Path
	Uuid     []byte
}

type DeletePathResponse struct {
}

type AddNeighborRequest struct {
	Peer *Peer
}

type AddNeighborResponse struct {
}

type DeleteNeighborRequest struct {
	Peer *Peer
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
	Direction ApiSoftResetNeighborRequest_SoftResetDirection
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
	ApiResource ApiResource
	VrfId       string
	Paths       []*Path
}

type InjectMrtResponse struct {
}

type AddBmpRequest struct {
	Address string
	Port    uint32
	Type    ApiAddBmpRequest_MonitoringPolicy
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
	Vrfs []*Vrf
}

type AddVrfRequest struct {
	Vrf *Vrf
}

type AddVrfResponse struct {
}

type DeleteVrfRequest struct {
	Vrf *Vrf
}

type DeleteVrfResponse struct {
}

type GetDefinedSetRequest struct {
	Type ApiDefinedType
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

type ApiPath struct {
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

type ApiDestination struct {
	Prefix         string
	Paths          []*Path
	LongerPrefixes bool
}

type ApiTable struct {
	Type         ApiResource
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
	Type   ApiResource
	Family uint32
	Prefix string
}

type ValidateRibResponse struct {
}

type ApiPeer struct {
	Families       []uint32
	ApplyPolicy    *ApplyPolicy
	Conf           *PeerConf
	EbgpMultihop   *EbgpMultihop
	RouteReflector *RouteReflector
	Info           *PeerState
	Timers         *Timers
	Transport      *Transport
	RouteServer    *RouteServer
}

type ApplyPolicy struct {
	InPolicy     *PolicyAssignment
	ExportPolicy *PolicyAssignment
	ImportPolicy *PolicyAssignment
}

type PrefixLimit struct {
	Family               uint32
	MaxPrefixes          uint32
	ShutdownThresholdPct uint32
}

type ApiPeerConf struct {
	AuthPassword     string
	Description      string
	LocalAs          uint32
	NeighborAddress  string
	PeerAs           uint32
	PeerGroup        string
	PeerType         uint32
	RemovePrivateAs  uint32
	RouteFlapDamping bool
	SendCommunity    uint32
	RemoteCap        [][]byte
	LocalCap         [][]byte
	Id               string
	PrefixLimits     []*PrefixLimit
}

type EbgpMultihop struct {
	Enabled     bool
	MultihopTtl uint32
}

type RouteReflector struct {
	RouteReflectorClient    bool
	RouteReflectorClusterId string
}

type ApiPeerState struct {
	AuthPassword          string
	Description           string
	LocalAs               uint32
	Messages              *Messages
	NeighborAddress       string
	PeerAs                uint32
	PeerGroup             string
	PeerType              uint32
	Queues                *Queues
	RemovePrivateAs       uint32
	RouteFlapDamping      bool
	SendCommunity         uint32
	SessionState          uint32
	SupportedCapabilities []string
	BgpState              string
	AdminState            string
	Received              uint32
	Accepted              uint32
	Advertised            uint32
	OutQ                  uint32
	Flops                 uint32
}

type Messages struct {
	Received *Message
	Sent     *Message
}

type Message struct {
	NOTIFICATION uint64
	UPDATE       uint64
	OPEN         uint64
	KEEPALIVE    uint64
	REFRESH      uint64
	DISCARDED    uint64
	TOTAL        uint64
}

type Queues struct {
	Input  uint32
	Output uint32
}

type Timers struct {
	Config *TimersConfig
	State  *TimersState
}

type TimersConfig struct {
	ConnectRetry                 uint64
	HoldTime                     uint64
	KeepaliveInterval            uint64
	MinimumAdvertisementInterval uint64
}

type TimersState struct {
	ConnectRetry                 uint64
	HoldTime                     uint64
	KeepaliveInterval            uint64
	MinimumAdvertisementInterval uint64
	NegotiatedHoldTime           uint64
	Uptime                       uint64
	Downtime                     uint64
}

type Transport struct {
	LocalAddress  string
	LocalPort     uint32
	MtuDiscovery  bool
	PassiveMode   bool
	RemoteAddress string
	RemotePort    uint32
	TcpMss        uint32
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
	Type     ApiDefinedType
	Name     string
	List     []string
	Prefixes []*Prefix
}

type MatchSet struct {
	Type ApiMatchType
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
	Type        ApiCommunityActionType
	Communities []string
}

type MedAction struct {
	Type  ApiMedActionType
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
	RouteAction  ApiRouteAction
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
	Type     ApiPolicyType
	Resource ApiResource
	Name     string
	Policies []*Policy
	Default  ApiRouteAction
}

type ApiRoa struct {
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

type Vrf struct {
	Name     string
	Rd       []byte
	ImportRt [][]byte
	ExportRt [][]byte
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
