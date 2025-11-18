import sys
sys.path.append('..')

import pytest
from main.protocolcheck import ProtocolCheck

test_targets = [
    {
        "ip_address": "172.20.20.2",
        "username": "admin",
        "password": "NokiaSrl1!",
        "community": "public",
    }
]

def test_run():
    """基本的な実行テスト"""
    p = ProtocolCheck(test_targets)
    p.run()
    # inventoryに何かしらの結果が入っていることを確認
    assert len(p.inventory) >= 0

def test_gnmi_available():
    """gNMI接続可能性のテスト"""
    p = ProtocolCheck(test_targets)
    target = test_targets[0]

    # ポート57401では接続成功を期待
    result_57401 = p.gnmi_available(target, 57401)
    print(f"Port 57401 result: {result_57401}")

    # ポート57400では接続失敗を期待
    result_57400 = p.gnmi_available(target, 57400)
    print(f"Port 57400 result: {result_57400}")

    # アサーション
    assert result_57401 == True, "Port 57401 should be available"
    assert result_57400 == False, "Port 57400 should be unavailable"

def test_gnmi_individual_ports():
    """個別ポートのテスト"""
    p = ProtocolCheck(test_targets)
    target = test_targets[0]

    # 57401のテスト
    assert p.gnmi_available(target, 57401) == True

    # 57400のテスト
    assert p.gnmi_available(target, 57400) == False


def test_snmp_available():
    p = ProtocolCheck(test_targets)
    target = test_targets[0]
    assert p.snmp_available(target) == True

if __name__ == "__main__":
    # 直接実行する場合のテスト
    p = ProtocolCheck(test_targets)
    target = test_targets[0]

    print("Testing port 57401...")
    result1 = p.gnmi_available(target, 57401)
    print(f"Result: {result1}")

    print("\nTesting port 57400...")
    result2 = p.gnmi_available(target, 57400)
    print(f"Result: {result2}")

    print(f"\nFinal results: 57401={result1}, 57400={result2}")
