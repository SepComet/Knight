"""Schema/serialization regression tests, not a server-side validation implementation.

Run from any directory with protoc on PATH and protobuf==6.33.0 installed:
    python Knight-Tools/test-proto.py
"""

from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

from google.protobuf import descriptor_pb2, descriptor_pool, message_factory


ROOT = Path(__file__).resolve().parents[1]
# MsgId numbers and Envelope tags are independent wire namespaces; preserve both.
MESSAGES = {
    "handshake_req": (1, 10), "handshake_ack": (2, 11),
    "heartbeat": (3, 12), "error": (4, 13),
    "create_room_req": (32, 20), "join_room_req": (33, 21),
    "leave_room": (34, 22), "set_ready": (35, 23),
    "start_game": (36, 24), "room_state_push": (37, 25),
    "game_start": (38, 26), "entity_spawn": (64, 30),
    "entity_despawn": (65, 31), "damage_event": (66, 32),
    "door_state": (67, 33), "room_transition": (68, 34),
    "room_loaded": (69, 35), "item_picked": (70, 36),
    "player_left": (71, 37), "game_result": (72, 38),
    "snapshot": (96, 40), "input_batch": (97, 41),
}


class ProtocolTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        protoc = shutil.which("protoc")
        if not protoc:
            raise RuntimeError("protoc must be installed and on PATH")
        with tempfile.TemporaryDirectory(prefix="knight-descriptor-") as temp:
            output = Path(temp) / "knight.pb"
            subprocess.run([
                protoc, f"-I{ROOT / 'Knight-Proto'}",
                f"--descriptor_set_out={output}", "knight.proto",
            ], check=True)
            files = descriptor_pb2.FileDescriptorSet.FromString(output.read_bytes())
        cls.schema = files.file[0]
        cls.pool = descriptor_pool.DescriptorPool()
        cls.pool.Add(cls.schema)

    def message(self, name, **fields):
        descriptor = self.pool.FindMessageTypeByName(f"knight.{name}")
        return message_factory.GetMessageClass(descriptor)(**fields)

    def round_trip(self, message):
        result = type(message).FromString(message.SerializeToString())
        self.assertEqual(result, message)
        return result

    def test_language_options(self):
        self.assertEqual(self.schema.package, "knight")
        self.assertEqual(self.schema.syntax, "proto3")
        self.assertEqual(self.schema.options.csharp_namespace, "Knight.Proto")
        # Still provisional until the server module is established (protocol §12).
        self.assertEqual(self.schema.options.go_package, "knight-server/protogen")

    def test_message_ids_and_envelope_tags(self):
        ids = self.pool.FindEnumTypeByName("knight.MsgId").values_by_name
        payload = self.message("Envelope").DESCRIPTOR.oneofs_by_name["payload"]
        self.assertEqual(set(ids), {"MSG_NONE"} | {
            f"MSG_{name.upper()}" for name in MESSAGES
        })
        self.assertEqual({field.name for field in payload.fields}, set(MESSAGES))
        for name, (msg_id, tag) in MESSAGES.items():
            with self.subTest(message=name):
                self.assertEqual(ids[f"MSG_{name.upper()}"].number, msg_id)
                envelope = self.message("Envelope", msg_id=msg_id)
                getattr(envelope, name).SetInParent()  # Includes empty commands.
                self.assertEqual(envelope.DESCRIPTOR.fields_by_name[name].number, tag)
                self.assertEqual(self.round_trip(envelope).WhichOneof("payload"), name)

    def test_oneof_selects_exactly_one_payload(self):
        envelope = self.message("Envelope", msg_id=3)
        envelope.heartbeat.SetInParent()
        envelope.handshake_req.proto_version = 2
        self.assertFalse(envelope.HasField("heartbeat"))
        self.assertEqual(envelope.WhichOneof("payload"), "handshake_req")
        # msg_id is a separate field: receivers must still validate its match.
        self.assertEqual(envelope.msg_id, 3)

    def test_entity_fields_preserve_tags_and_types(self):
        field_type = descriptor_pb2.FieldDescriptorProto
        expected = {
            "entity_id": (1, field_type.TYPE_UINT32),
            "type": (2, field_type.TYPE_ENUM),
            "pos_x": (3, field_type.TYPE_FLOAT),
            "pos_y": (4, field_type.TYPE_FLOAT),
            "facing": (5, field_type.TYPE_FLOAT),
            "state": (6, field_type.TYPE_ENUM),
            "hp": (7, field_type.TYPE_INT32),
            "player_uid": (8, field_type.TYPE_UINT64),
            "config_id": (9, field_type.TYPE_UINT32),
        }
        actual = self.message("EntityState").DESCRIPTOR.fields
        self.assertEqual({f.name: (f.number, f.type) for f in actual}, expected)

    def test_snapshot_contains_player_identity_and_distinct_enemy_configs(self):
        # uid deliberately differs from entity_id and exceeds 32-bit range.
        uid = (1 << 63) + 17
        snapshot = self.message("Snapshot", tick=30, last_input_seq=29, entities=[
            {"entity_id": 7, "type": 1, "player_uid": uid, "config_id": 1,
             "pos_x": 1.25, "pos_y": -2.5, "hp": 100},
            {"entity_id": 8, "type": 2, "config_id": 1},
            {"entity_id": 9, "type": 2, "config_id": 2},
        ])
        decoded = self.round_trip(snapshot)
        self.assertEqual(decoded.entities[0].player_uid, uid)
        self.assertNotEqual(decoded.entities[0].entity_id, uid)
        self.assertEqual([e.config_id for e in decoded.entities[1:]], [1, 2])
        self.assertTrue(all(e.player_uid == 0 for e in decoded.entities[1:]))
        spawned = self.message("EntitySpawn", entities=snapshot.entities)
        self.assertEqual(self.round_trip(spawned).entities, decoded.entities)

    def test_loading_ack_carries_map_correlation(self):
        field = self.message("RoomLoaded").DESCRIPTOR.fields_by_name["map_id"]
        self.assertEqual((field.number, field.type), (1, field.TYPE_UINT32))
        transition = self.message("RoomTransition", map_id=2, spawns=[
            {"uid": 17, "pos_x": 1.25, "pos_y": 2.5},
        ])
        ack = self.round_trip(self.message("RoomLoaded", map_id=transition.map_id))
        stale = self.round_trip(self.message("RoomLoaded", map_id=1))
        self.assertEqual(ack.map_id, transition.map_id)
        self.assertNotEqual(stale.map_id, transition.map_id)

    def test_damage_amount_does_not_depend_on_snapshot_hp(self):
        field = self.message("DamageEvent").DESCRIPTOR.fields_by_name["damage"]
        self.assertEqual((field.number, field.type), (6, field.TYPE_INT32))
        hit = self.round_trip(self.message(
            "DamageEvent", attacker_id=8, target_id=7, new_hp=0, damage=3,
            pos_x=1.25, pos_y=-2.5,
        ))
        self.assertEqual((hit.new_hp, hit.damage), (0, 3))

    def test_input_batch_and_result_round_trips(self):
        batch = self.message("InputBatch", first_seq=1, inputs=[
            {"seq": 1, "move_x": 1, "aim_angle": 90, "buttons": 3},
            {"seq": 2, "move_y": -1, "buttons": 1},
        ])
        self.assertEqual(len(self.round_trip(batch).inputs), 2)
        result = self.message("GameResult", match_id=1 << 40, outcome=1,
                              duration_sec=120, results=[
                                  {"uid": 17, "kills": 5, "damage_dealt": 100,
                                   "damage_taken": 20, "alive": True},
                              ])
        self.assertTrue(self.round_trip(result).results[0].alive)

    def test_protocol_version_is_synchronized(self):
        proto = (ROOT / "Knight-Proto/knight.proto").read_text(encoding="utf-8")
        doc = (ROOT / "Docs/protocol.md").read_text(encoding="utf-8")
        self.assertEqual(re.search(r"PROTO_VERSION = (\d+)", proto).group(1), "2")
        self.assertRegex(doc, r"\| `proto_version` \| 2 \|")
        self.assertIn("`proto_version = 2`", doc)


if __name__ == "__main__":
    unittest.main(verbosity=2)
