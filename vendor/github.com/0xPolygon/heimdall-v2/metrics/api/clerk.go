package api

const (
	// Query API methods.
	GetRecordCountMethod        = "GetRecordCount"
	GetRecordListMethod         = "GetRecordList"
	GetLatestRecordIdMethod     = "GetLatestRecordId"
	GetRecordByIdMethod         = "GetRecordById"
	GetRecordListWithTimeMethod = "GetRecordListWithTime"
	GetRecordSequenceMethod     = "GetRecordSequence"
	IsClerkTxOldMethod          = "IsClerkTxOld"

	// Transaction API methods.
	HandleMsgEventRecordMethod = "HandleMsgEventRecord"

	// Side message handler methods.
	SideHandleMsgEventRecordMethod = "SideHandleMsgEventRecord"

	// Post message handler methods.
	PostHandleMsgEventRecordMethod = "PostHandleMsgEventRecord"
)
