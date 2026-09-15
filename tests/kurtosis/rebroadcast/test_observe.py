import os
import pathlib
import subprocess
import unittest


STUBS = r'''
exec 3>&2
python3() { echo target-container; }
kurtosis() {
  echo "kurtosis $*" >&3
  case "$*" in
    *l2-tx-spammer*) echo 'PRIVATE_KEY: 0x1234' ;;
    'port print '*) echo http://127.0.0.1:18545 ;;
    'service logs '*) echo 'Rebroadcast stuck transactions' ;;
    *) return 1 ;;
  esac
}
docker() {
  echo "docker $*" >&3
  case "$*" in
    *'wallet address --private-key dedicated-key') echo dedicated-sender ;;
    *'wallet address '*) echo spammer-sender ;;
    *'send --async '*) echo observation-hash; return "${SEND_STATUS:-0}" ;;
    *'qdisc '*) return 0 ;;
    *) return 1 ;;
  esac
}
curl() {
  echo "curl $*" >&3
  echo "${NONCE_RESPONSE}"
}
source "$1"
'''


class ObservationTest(unittest.TestCase):
    def observe(self, **overrides):
        env = dict(os.environ, OBSERVATION_PRIVATE_KEY="dedicated-key",
                   SEED_TX="true", DURATION="0", SEND_STATUS="0",
                   NONCE_RESPONSE='{"result":"0x2a"}')
        env.update(overrides)
        script = pathlib.Path(__file__).with_name("observe.sh")
        return subprocess.run(["bash", "-c", STUBS, "bash", str(script)],
                              env=env, text=True, capture_output=True, timeout=10)

    def test_seed_uses_dedicated_account_and_pending_nonce(self):
        result = self.observe()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('"params":["dedicated-sender","pending"]', result.stderr)
        self.assertIn("--private-key dedicated-key --legacy --nonce 42", result.stderr)
        self.assertNotIn("l2-tx-spammer", result.stderr)
        self.assertIn("Submitted observation transaction observation-hash", result.stdout)

    def test_missing_key_fails_before_accessing_the_network(self):
        result = self.observe(OBSERVATION_PRIVATE_KEY="")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("dedicated funded devnet account", result.stderr)
        self.assertNotIn("docker ", result.stderr)
        self.assertNotIn("kurtosis ", result.stderr)

    def test_observation_without_seeding_needs_no_key(self):
        result = self.observe(SEED_TX="false", OBSERVATION_PRIVATE_KEY="")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("wallet address", result.stderr)
        self.assertNotIn("send --async", result.stderr)
        self.assertIn("qdisc add", result.stderr)
        self.assertIn("qdisc del", result.stderr)

    def test_seed_failure_aborts_before_adding_delay(self):
        for overrides in ({"SEND_STATUS": "1"}, {"NONCE_RESPONSE": '{"error":"unavailable"}'}):
            with self.subTest(overrides=overrides):
                result = self.observe(**overrides)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("qdisc add", result.stderr)
                self.assertNotIn("Submitted observation transaction", result.stdout)


if __name__ == "__main__":
    unittest.main()
