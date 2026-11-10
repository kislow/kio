from ki_kb import sections

DOC = """---
title: Guide
---
intro text

# Guide
top

## Setup
install

### Linux
apt

## Usage
run
"""


def test_nested_headings_get_a_heading_path_key():
    got = sections.split("docs/g.md", DOC)
    assert [s.key for s in got] == [
        "docs/g.md#",
        "docs/g.md#Guide",
        "docs/g.md#Guide > Setup",
        "docs/g.md#Guide > Setup > Linux",
        "docs/g.md#Guide > Usage",
    ]
    assert got[3].text == "### Linux\napt\n"


def test_front_matter_belongs_to_the_text_before_the_first_heading():
    got = sections.split("g.md", "---\ntitle: T\n---\nintro\n# H\nx\n")
    assert [s.key for s in got] == ["g.md#", "g.md#H"]
    assert got[0].text == "---\ntitle: T\n---\nintro"
    assert sections.split("g.md", "# H\nx\n")[0].key == "g.md#H"
    assert sections.title_of("docs/g.md", DOC) == "Guide"


def test_a_document_with_no_headings_is_one_section():
    got = sections.split("notes.md", "just\ntext\n")
    assert [(s.key, s.heading_path) for s in got] == [("notes.md#", "")]
    assert sections.title_of("docs/notes.md", "just text") == "notes.md"


def test_headings_inside_code_fences_do_not_split():
    text = "# A\n```sh\n# not a heading\n```\n~~~\n## nor this\n~~~\n## B\n"
    assert [s.key for s in sections.split("a.md", text)] == ["a.md#A"]


def test_repeated_heading_paths_stay_unique():
    got = sections.split("a.md", "# A\n## Notes\nx\n## Notes\ny\n")
    assert [s.key for s in got] == ["a.md#A > Notes", "a.md#A > Notes~2"]


def test_a_heading_with_only_subheadings_is_not_a_section():
    text = "# Title\n\n## Why\nbecause\n## How\n\n### Steps\none\n"
    got = sections.split("a.md", text)
    assert [s.key for s in got] == ["a.md#Title > Why", "a.md#Title > How > Steps"]
    assert got[0].text == "## Why\nbecause"
    assert sections.split("a.md", "# Title\n") == []


def test_empty_document_has_no_sections():
    assert sections.split("a.md", "\n  \n") == []


def test_hash_ignores_whitespace_and_wrapping_but_not_words():
    a = sections.normalized_hash("# A\nsome   long\nline\n")
    assert a == sections.normalized_hash("# A\n\nsome long line")
    assert a != sections.normalized_hash("# A\nsome longer line")
