import pathlib, shutil, subprocess, unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
NODE = shutil.which("node")

@unittest.skipUnless(NODE, "Node.js is required for the browser codec regression")
class PasskeyOptionsTests(unittest.TestCase):
    def test_retry_preserves_login_and_registration_options(self):
        source = (ROOT / "internal/ownerweb/passkey_portal.go").read_text()
        codec = "\n".join(line for line in source.splitlines() if line.startswith(
            ("const b64bytes=", "const unwrap=", "const createOptions=", "const getOptions=")))
        check = """
const assert=require('node:assert/strict');
for (const wrapped of [true,false]) {
  for (const registration of [false,true]) {
    const p={challenge:'AQID'};
    if(registration){p.user={id:'BAUG'};p.excludeCredentials=[{type:'public-key',id:'BwgJ'}];}
    else p.allowCredentials=[{type:'public-key',id:'BwgJ'}];
    const options=wrapped?{publicKey:p}:p;
    const before=JSON.stringify(options);
    const convert=registration?createOptions:getOptions;
    const first=convert(options), second=convert(options);
    assert.deepEqual([...first.publicKey.challenge],[1,2,3]);
    assert.deepEqual([...second.publicKey.challenge],[1,2,3]);
    assert.equal(JSON.stringify(options),before);
    if(registration)assert.deepEqual([...second.publicKey.user.id],[4,5,6]);
    const credentials=registration?second.publicKey.excludeCredentials:second.publicKey.allowCredentials;
    assert.deepEqual([...credentials[0].id],[7,8,9]);
  }
}
"""
        result = subprocess.run([NODE, "-e", codec + "\n" + check],
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

if __name__ == "__main__":
    unittest.main()
