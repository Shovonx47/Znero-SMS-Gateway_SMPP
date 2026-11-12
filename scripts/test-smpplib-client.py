"""Independent third-party SMPP client test using Python smpplib against live Znero Customer SMPP Gateway."""

import argparse
import sys
import time
import smpplib.client
import smpplib.consts


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=2776)
    parser.add_argument("--system-id", required=True)
    parser.add_argument("--password", required=True)
    parser.add_argument("--sender", default="ZNERO")
    args = parser.parse_args()

    sent_ids = []
    dlrs = []

    client = smpplib.client.Client(args.host, args.port, timeout=5)

    def handle_submit_resp(pdu):
        if pdu.status != smpplib.consts.SMPP_ESME_ROK:
            raise RuntimeError(f"submit_sm_resp error status={pdu.status}")
        mid = pdu.message_id
        if isinstance(mid, bytes):
            mid = mid.decode("ascii", errors="replace")
        sent_ids.append(mid)
        print(f"[smpplib] submit_sm_resp seq={pdu.sequence} message_id={mid}")

    def handle_deliver_sm(pdu):
        body = pdu.short_message or b""
        if isinstance(body, bytes):
            body_text = body.decode("utf-8", errors="replace")
        else:
            body_text = str(body)
        print(f"[smpplib] deliver_sm esm={pdu.esm_class:#x} msg={body_text}")
        if (pdu.esm_class & 0x04) != 0 and "stat:DELIVRD" in body_text:
            dlrs.append(body_text)
        # smpplib auto-responds deliver_sm_resp unless overridden, return None

    client.set_message_sent_handler(handle_submit_resp)
    client.set_message_received_handler(handle_deliver_sm)

    client.connect()
    client.bind_transceiver(system_id=args.system_id, password=args.password)
    print(f"[smpplib] bound transceiver cleanly as {args.system_id} on {args.host}:{args.port}")

    try:
        # 1. GSM-7 message (smpplib expects str for C-octet address fields, bytes for short_message)
        client.send_message(
            source_addr_ton=smpplib.consts.SMPP_TON_ALNUM,
            source_addr_npi=smpplib.consts.SMPP_NPI_UNK,
            source_addr=args.sender,
            dest_addr_ton=smpplib.consts.SMPP_TON_INTL,
            dest_addr_npi=smpplib.consts.SMPP_NPI_ISDN,
            destination_addr="+12025557001",
            short_message=b"Hello from Python smpplib!",
            registered_delivery=0x01,  # SMPP v3.4 bit 0: SMSC delivery receipt requested
        )

        # 2. UCS-2 UTF-16BE message
        ucs_payload = "Py ধন্যবাদ مرحبا".encode("utf-16-be")
        client.send_message(
            source_addr_ton=smpplib.consts.SMPP_TON_ALNUM,
            source_addr_npi=smpplib.consts.SMPP_NPI_UNK,
            source_addr=args.sender,
            dest_addr_ton=smpplib.consts.SMPP_TON_INTL,
            dest_addr_npi=smpplib.consts.SMPP_NPI_ISDN,
            destination_addr="+12025557002",
            short_message=ucs_payload,
            data_coding=0x08,
            registered_delivery=0x01,
        )

        deadline = time.time() + 20.0
        while time.time() < deadline:
            if len(sent_ids) >= 2 and len(dlrs) >= 2:
                break
            try:
                client.read_once()
            except Exception as exc:
                # socket timeout during read_once is normal when waiting
                if "timed out" not in str(exc).lower():
                    raise

        if len(sent_ids) < 2:
            print(f"FAIL: expected >=2 submit_sm_resp, got {len(sent_ids)}", file=sys.stderr)
            return 1
        if len(dlrs) < 2:
            print(f"FAIL: expected >=2 deliver_sm DLRs, got {len(dlrs)} (dlrs={dlrs})", file=sys.stderr)
            return 1

        print(f"[smpplib PASS] submitted={len(sent_ids)} DLRs_received={len(dlrs)}")
        return 0
    finally:
        try:
            client.unbind()
        except Exception:
            pass
        try:
            client.disconnect()
        except Exception:
            pass


if __name__ == "__main__":
    sys.exit(main())

