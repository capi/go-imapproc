"""Tests for build_multipart_body."""

from conftest import SAMPLE_EMAIL, mod


class TestBuildMultipartBody:
    def test_content_type_header_contains_boundary(self):
        _, ct = mod.build_multipart_body({}, SAMPLE_EMAIL)
        assert ct.startswith("multipart/related; boundary=")

    def test_body_contains_json_part(self):
        metadata = {"labelIds": ["INBOX"]}
        body, _ = mod.build_multipart_body(metadata, SAMPLE_EMAIL)
        body_str = body.decode()
        assert "Content-Type: application/json" in body_str
        assert '"labelIds"' in body_str
        assert '"INBOX"' in body_str

    def test_body_contains_rfc822_part(self):
        body, _ = mod.build_multipart_body({}, SAMPLE_EMAIL)
        assert b"Content-Type: message/rfc822" in body
        assert SAMPLE_EMAIL in body

    def test_boundary_used_as_delimiter(self):
        body, ct = mod.build_multipart_body({}, SAMPLE_EMAIL)
        boundary = ct.split("boundary=")[1]
        assert boundary.encode() in body

    def test_closing_boundary_present(self):
        body, ct = mod.build_multipart_body({}, SAMPLE_EMAIL)
        boundary = ct.split("boundary=")[1]
        assert f"--{boundary}--".encode() in body

    def test_empty_metadata_serialises_to_empty_object(self):
        body, _ = mod.build_multipart_body({}, SAMPLE_EMAIL)
        assert b"{}" in body

    def test_boundary_is_unique_across_calls(self):
        _, ct1 = mod.build_multipart_body({}, SAMPLE_EMAIL)
        _, ct2 = mod.build_multipart_body({}, SAMPLE_EMAIL)
        assert ct1 != ct2
