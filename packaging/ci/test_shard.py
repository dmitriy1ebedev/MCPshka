import unittest

import shard


class ShardTest(unittest.TestCase):
    plan = {"sites": {"a": {"speed": 1.0, "suites": ["race"]},
                      "b": {"speed": 1.0, "suites": ["race", "fuzz"]},
                      "c": {"speed": 1.0, "suites": ["e2e"]}},
            "weights": {"m/x": 70, "m/y": 30, "m/z": 20, "m/w": 10, "m/v": 10}}
    pkgs = ["m/x", "m/y", "m/z", "m/w", "m/v", "m/new"]

    def test_partition_has_no_overlap_and_covers_all(self):
        a = shard.shard(self.plan, "race", "a", self.pkgs)
        b = shard.shard(self.plan, "race", "b", self.pkgs)
        self.assertFalse(set(a) & set(b))
        self.assertEqual(sorted(a + b), sorted(self.pkgs))

    def test_balanced_by_weight(self):
        w = dict(self.plan["weights"], **{"m/new": 20})  # unknown gets the median
        loads = [sum(w[p] for p in shard.shard(self.plan, "race", s, self.pkgs)) for s in "ab"]
        self.assertLessEqual(abs(loads[0] - loads[1]), 10)  # 160s split 80/80

    def test_deterministic(self):
        self.assertEqual(shard.shard(self.plan, "race", "b", self.pkgs),
                         shard.shard(self.plan, "race", "b", list(reversed(self.pkgs))))

    def test_offline_site_share_moves_to_the_rest(self):
        self.assertEqual(sorted(shard.shard(self.plan, "race", "b", self.pkgs, active=["b"])), sorted(self.pkgs))
        self.assertEqual(shard.shard(self.plan, "race", "a", self.pkgs, active=["b"]), [])

    def test_site_outside_suite_gets_nothing_and_empty_suite_refuses(self):
        self.assertEqual(shard.shard(self.plan, "race", "c", self.pkgs), [])
        with self.assertRaises(ValueError):
            shard.shard(self.plan, "race", "a", self.pkgs, active=["c"])

    def test_speed_shifts_load(self):
        fast = {"a": 3.0, "b": 1.0}
        out = shard.assign(["p%d" % i for i in range(8)], {}, fast)
        self.assertGreater(len(out["a"]), len(out["b"]))

    def test_repository_plan_is_valid(self):
        plan = shard.load()
        self.assertEqual(plan["gate"]["site"], "gitverse")
        self.assertEqual(shard.sites_for(plan, "race"), ["github", "moshub"])
        self.assertTrue(all(v > 0 for v in plan["weights"].values()))


if __name__ == "__main__":
    unittest.main()
