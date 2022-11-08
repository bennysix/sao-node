package apitypes

type CreateResp struct {
	DataId string
	Alias  string
	TxId   string
	Cid    string
}

type UpdateResp struct {
	DataId   string
	CommitId string
	Alias    string
	TxId     string
	Cid      string
}

type LoadResp struct {
	DataId   string
	Alias    string
	CommitId string
	Version  string
	Cid      string
	Content  string
}

type DeleteResp struct {
	DataId string
	Alias  string
}

type ShowCommitsResp struct {
	DataId  string
	Alias   string
	Commits []string
}

type GetPeerInfoResp struct {
	PeerInfo string
}
