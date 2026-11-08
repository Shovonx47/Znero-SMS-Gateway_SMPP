package pdu

// SMPP 3.4 Command IDs
const (
	CmdGenericNack         uint32 = 0x80000000
	CmdBindReceiver        uint32 = 0x00000001
	CmdBindReceiverResp    uint32 = 0x80000001
	CmdBindTransmitter     uint32 = 0x00000002
	CmdBindTransmitterResp uint32 = 0x80000002
	CmdQuerySM             uint32 = 0x00000003
	CmdQuerySMResp         uint32 = 0x80000003
	CmdSubmitSM            uint32 = 0x00000004
	CmdSubmitSMResp        uint32 = 0x80000004
	CmdDeliverSM           uint32 = 0x00000005
	CmdDeliverSMResp       uint32 = 0x80000005
	CmdUnbind              uint32 = 0x00000006
	CmdUnbindResp          uint32 = 0x80000006
	CmdReplaceSM           uint32 = 0x00000007
	CmdReplaceSMResp       uint32 = 0x80000007
	CmdCancelSM            uint32 = 0x00000008
	CmdCancelSMResp        uint32 = 0x80000008
	CmdBindTransceiver     uint32 = 0x00000009
	CmdBindTransceiverResp uint32 = 0x80000009
	CmdEnquireLink         uint32 = 0x00000015
	CmdEnquireLinkResp     uint32 = 0x80000015
	CmdSubmitMulti         uint32 = 0x00000021
	CmdSubmitMultiResp     uint32 = 0x80000021
)

// SMPP 3.4 Command Status Codes
const (
	StatusROK              uint32 = 0x00000000 // No Error
	StatusRInvMsgLen       uint32 = 0x00000001 // Message Length is invalid
	StatusRInvCmdLen       uint32 = 0x00000002 // Command Length is invalid
	StatusRInvCmdID        uint32 = 0x00000003 // Invalid Command ID
	StatusRInvBndSts       uint32 = 0x00000004 // Incorrect BIND Status for given command
	StatusRAlyBnd          uint32 = 0x00000005 // ESME Already in Bound State
	StatusRInvPrtFlg       uint32 = 0x00000006 // Invalid Priority Flag
	StatusRInvRegDlvFlg    uint32 = 0x00000007 // Invalid Registered Delivery Flag
	StatusRSysErr          uint32 = 0x00000008 // System Error
	StatusRInvSrcAdr       uint32 = 0x0000000A // Invalid Source Address
	StatusRInvDstAdr       uint32 = 0x0000000B // Invalid Dest Addr
	StatusRInvMsgID        uint32 = 0x0000000C // Message ID is invalid
	StatusRBindFail        uint32 = 0x0000000D // Bind Failed
	StatusRInvPaswd        uint32 = 0x0000000E // Invalid Password
	StatusRInvSysID        uint32 = 0x0000000F // Invalid System ID
	StatusRCancelFail      uint32 = 0x00000011 // Cancel SM Failed
	StatusRReplaceFail     uint32 = 0x00000013 // Replace SM Failed
	StatusRMsgQFul         uint32 = 0x00000014 // Message Queue Full
	StatusRServiceType     uint32 = 0x00000015 // Invalid Service Type
	StatusRInvDestFlag     uint32 = 0x00000033 // Invalid number of destinations
	StatusRInvSubRep       uint32 = 0x00000034 // Invalid distribution list name
	StatusRInvDestType     uint32 = 0x00000040 // Destination flag is invalid
	StatusRInvNumDests     uint32 = 0x00000042 // Invalid submit_multi destinations
	StatusRThrottled       uint32 = 0x00000058 // ESME Receiver/Transmitter Throttled
	StatusRInvSched        uint32 = 0x00000061 // Invalid Scheduled Delivery Time
	StatusRInvExpiry       uint32 = 0x00000062 // Invalid message (expiry) validity period
	StatusRInvDCS          uint32 = 0x00000063 // Predefined Data Coding Scheme Invalid
	StatusRInvOptParStream uint32 = 0x000000C0 // Error in the optional part of the PDU Body
	StatusROptParNotAllwd  uint32 = 0x000000C1 // Optional Parameter not allowed
	StatusRInvParLen       uint32 = 0x000000C2 // Invalid Parameter Length
	StatusRMissingOptParam uint32 = 0x000000C3 // Expected Optional Parameter missing
	StatusRInvOptParamVal  uint32 = 0x000000C4 // Invalid Optional Parameter Value
	StatusRDeliveryFailure uint32 = 0x000000FE // Delivery Failure (used for data_sm_resp)
	StatusRUnknownErr      uint32 = 0x000000FF // Unknown Error
)

// SMPP 3.4 Type of Number (TON)
const (
	TONUnknown          uint8 = 0
	TONInternational    uint8 = 1
	TONNational         uint8 = 2
	TONNetworkSpecific  uint8 = 3
	TONSubscriberNumber uint8 = 4
	TONAlphanumeric     uint8 = 5
	TONAbbreviated      uint8 = 6
)

// SMPP 3.4 Numbering Plan Indicator (NPI)
const (
	NPIUnknown    uint8 = 0
	NPIISDN       uint8 = 1 // E.163 / E.164
	NPIData       uint8 = 3 // X.121
	NPITelex      uint8 = 4
	NPILandMobile uint8 = 6 // E.212
	NPINational   uint8 = 8
	NPISubscriber uint8 = 9
	NPIERMES      uint8 = 10
	NPIInternet   uint8 = 14 // IP
	NPIWAP        uint8 = 18
)

// Data Coding
const (
	DataCodingSMSCDefault uint8 = 0 // GSM 7-bit default
	DataCodingASCII       uint8 = 1 // IA5 / ASCII
	DataCoding8BitBinary  uint8 = 4 // 8-bit binary data
	DataCodingUCS2        uint8 = 8 // UCS-2 (UTF-16BE)
)

// ESM Class Flags
const (
	ESMClassDefault         uint8 = 0x00
	ESMClassDatagram        uint8 = 0x01
	ESMClassForward         uint8 = 0x02
	ESMClassStoreAndForward uint8 = 0x03
	ESMClassUDHIIndicator   uint8 = 0x40 // UDHI: User Data Header Indicator
	ESMClassReplyPath       uint8 = 0x80
)

// Registered Delivery Flags
const (
	RegDeliveryNone                uint8 = 0x00
	RegDeliverySMSCDeliveryReceipt uint8 = 0x01
	RegDeliverySMEOriginatedAck    uint8 = 0x04
	RegDeliveryIntermediateNotify  uint8 = 0x10
)

// SMPP 3.4 TLV Tags (Optional Parameters)
const (
	TagDestAddrSubunit          uint16 = 0x0005
	TagDestNetworkType          uint16 = 0x0006
	TagDestBearerType           uint16 = 0x0007
	TagDestTelematicsId         uint16 = 0x0008
	TagSourceAddrSubunit        uint16 = 0x000D
	TagSourceNetworkType        uint16 = 0x000E
	TagSourceBearerType         uint16 = 0x000F
	TagSourceTelematicsId       uint16 = 0x0010
	TagQosTimeToLive            uint16 = 0x0017
	TagPayloadType              uint16 = 0x0019
	TagAdditionalStatusInfoText uint16 = 0x001D
	TagReceiptedMessageId       uint16 = 0x001E
	TagMsMsgWaitFacilities      uint16 = 0x0030
	TagPrivacyIndicator         uint16 = 0x0201
	TagSourceSubaddress         uint16 = 0x0202
	TagDestSubaddress           uint16 = 0x0203
	TagUserMessageReference     uint16 = 0x0204
	TagUserResponseCode         uint16 = 0x0205
	TagSourcePort               uint16 = 0x020A
	TagDestinationPort          uint16 = 0x020B
	TagSarMsgRefNum             uint16 = 0x020C
	TagSarTotalSegments         uint16 = 0x020E
	TagSarSegmentSeqnum         uint16 = 0x020F
	TagLanguageIndicator        uint16 = 0x0210
	TagCcppCapabilityIndicator  uint16 = 0x0211
	TagCallbackNum              uint16 = 0x0381
	TagCallbackNumPresInd       uint16 = 0x0302
	TagCallbackNumAtag          uint16 = 0x0303
	TagNumberNumberOfUnackMsgs  uint16 = 0x0304
	TagSmsSignal                uint16 = 0x0305
	TagAlertOnMsgDelivery       uint16 = 0x040C
	TagItsReplyType             uint16 = 0x0422
	TagItsSessionInfo           uint16 = 0x0423
	TagUssdServiceOp            uint16 = 0x0501
	TagMessagePayload           uint16 = 0x0424
	TagMessageState             uint16 = 0x0427
	TagNetworkErrorCode         uint16 = 0x0423
)

// HeaderLen is the fixed size of an SMPP PDU header in bytes
const HeaderLen = 16
