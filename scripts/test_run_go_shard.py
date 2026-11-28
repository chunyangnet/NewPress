import unittest

from run_go_shard import partition


class ShardTests(unittest.TestCase):
    def test_every_test_example_and_fuzz_seed_is_selected_exactly_once(self):
        names = ['TestZoo', 'FuzzURL', 'ExampleServer', 'TestOne', 'TestTwo', 'TestThree', 'TestFour']
        shards = [partition(names, index, 3) for index in range(3)]
        self.assertCountEqual([name for shard in shards for name in shard], names)
        self.assertLessEqual(max(map(len, shards)) - min(map(len, shards)), 1)
        self.assertEqual(partition(list(reversed(names)), 0, 3), shards[0])

    def test_invalid_or_empty_selection_cannot_silently_pass(self):
        for names, index, total in [([], 0, 1), (['TestA'], 1, 2), (['TestA'], 2, 2), (['TestA'], -1, 2), (['TestA', 'TestA'], 0, 1)]:
            with self.subTest(names=names, index=index, total=total), self.assertRaises(ValueError):
                partition(names, index, total)


if __name__ == '__main__':
    unittest.main()
