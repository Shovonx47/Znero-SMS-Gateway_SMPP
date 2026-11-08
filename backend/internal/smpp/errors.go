package smpp

import (
	"fmt"

	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

// ClassifyStatus maps an SMPP command_status to messaging.SendStatus based on SMPP 3.4.
func ClassifyStatus(status uint32) messaging.SendStatus {
	switch status {
	case pdu.StatusROK:
		return messaging.SendStatusAccepted

	// Retryable on another route: capacity, rate limiting, temporary server issues
	case pdu.StatusRThrottled, // 0x58: ESME Receiver/Transmitter Throttled
		pdu.StatusRMsgQFul,    // 0x14: Message Queue Full
		pdu.StatusRSysErr:     // 0x08: System Error
		return messaging.SendStatusRetryableReject

	// Fatal rejections: permanent message or routing rejection that must NOT fail over
	case pdu.StatusRInvDstAdr,       // 0x0B: Invalid Dest Addr
		pdu.StatusRInvSrcAdr,       // 0x0A: Invalid Source Address
		pdu.StatusRInvMsgLen,       // 0x01: Message Length is invalid
		pdu.StatusRInvDCS,          // 0x63: Predefined Data Coding Scheme Invalid
		pdu.StatusRInvPrtFlg,       // 0x06: Invalid Priority Flag
		pdu.StatusRInvRegDlvFlg,    // 0x07: Invalid Registered Delivery Flag
		pdu.StatusRInvSched,        // 0x61: Invalid Scheduled Delivery Time
		pdu.StatusRInvExpiry,       // 0x62: Invalid message (expiry) validity period
		pdu.StatusRInvOptParStream, // 0xC0: Error in optional part
		pdu.StatusROptParNotAllwd,  // 0xC1: Optional parameter not allowed
		pdu.StatusRInvParLen,       // 0xC2: Invalid Parameter Length
		pdu.StatusRInvOptParamVal:  // 0xC4: Invalid Optional Parameter Value
		return messaging.SendStatusFatalReject

	// Connection-level or authorization errors (retryable on another route)
	case pdu.StatusRBindFail, // 0x0D: Bind Failed
		pdu.StatusRInvPaswd,  // 0x0E: Invalid Password
		pdu.StatusRInvSysID,  // 0x0F: Invalid System ID
		pdu.StatusRInvBndSts, // 0x04: Incorrect BIND Status
		pdu.StatusRAlyBnd:    // 0x05: ESME Already in Bound State
		return messaging.SendStatusRetryableReject

	default:
		// Any other unexpected carrier error code is treated as fatal reject to prevent loops
		return messaging.SendStatusFatalReject
	}
}

// StatusString returns a human-readable name for an SMPP status code.
func StatusString(status uint32) string {
	switch status {
	case pdu.StatusROK:
		return "ESME_ROK"
	case pdu.StatusRInvMsgLen:
		return "ESME_RINVMSGLEN"
	case pdu.StatusRInvCmdLen:
		return "ESME_RINVCMDLEN"
	case pdu.StatusRInvCmdID:
		return "ESME_RINVCMDID"
	case pdu.StatusRInvBndSts:
		return "ESME_RINVBNDSTS"
	case pdu.StatusRAlyBnd:
		return "ESME_RALYBND"
	case pdu.StatusRSysErr:
		return "ESME_RSYSERR"
	case pdu.StatusRInvSrcAdr:
		return "ESME_RINVSRCADR"
	case pdu.StatusRInvDstAdr:
		return "ESME_RINVDSTADR"
	case pdu.StatusRBindFail:
		return "ESME_RBINDFAIL"
	case pdu.StatusRInvPaswd:
		return "ESME_RINVPASWD"
	case pdu.StatusRInvSysID:
		return "ESME_RINVSYSID"
	case pdu.StatusRMsgQFul:
		return "ESME_RMSGQFUL"
	case pdu.StatusRThrottled:
		return "ESME_RTHROTTLED"
	case pdu.StatusRInvDCS:
		return "ESME_RINVDCS"
	default:
		return fmt.Sprintf("ESME_STATUS_0x%08X", status)
	}
}

// MaskPhoneNumber masks all but the last 4 digits of a destination number for privacy and PCI/GDPR compliance.
func MaskPhoneNumber(phone string) string {
	if len(phone) <= 4 {
		return "****"
	}
	maskLen := len(phone) - 4
	masked := make([]byte, len(phone))
	for i := 0; i < maskLen; i++ {
		masked[i] = '*'
	}
	copy(masked[maskLen:], phone[maskLen:])
	return string(masked)
}

