#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'Usage: %s <file.md> [--max-lines <n>] [--dry-run] [--force]\n' "$(basename "$0")"
}

die() {
  printf '%s\n' "$1" >&2
  exit 1
}

trim() {
  local value="$1"
  value="${value#${value%%[!$' \t\r\n']*}}"
  value="${value%${value##*[!$' \t\r\n']}}"
  printf '%s' "$value"
}

sanitize_excerpt() {
  awk '
    function trim(s) {
      sub(/^[[:space:]]+/, "", s)
      sub(/[[:space:]]+$/, "", s)
      return s
    }
    {
      text = text $0 " "
    }
    END {
      gsub(/\r/, " ", text)
      gsub(/\n/, " ", text)
      gsub(/\t/, " ", text)
      while (gsub(/  +/, " ", text)) {}
      text = trim(text)
      sub(/^Excerpt:[[:space:]]*/, "", text)
      sub(/^excerpt:[[:space:]]*/, "", text)
      sub(/^[-*][[:space:]]+/, "", text)
      sub(/^[0-9]+[.)][[:space:]]+/, "", text)
      gsub(/`/, "", text)
      text = trim(text)
      if (text ~ /^".*"$/ && length(text) >= 2) {
        text = substr(text, 2, length(text) - 2)
      } else if (text ~ /^'"'"'.*'"'"'$/ && length(text) >= 2) {
        text = substr(text, 2, length(text) - 2)
      }
      text = trim(text)
      word_count = split(text, words, /[[:space:]]+/)
      if (text == "") {
        print ""
        exit
      }
      if (word_count > 45) {
        text = ""
        for (i = 1; i <= 45; i++) {
          text = text (i == 1 ? "" : " ") words[i]
        }
        text = text "..."
      }
      print text
    }
  ' <<< "$1"
}

summarize_part() {
  local part_path="$1"
  local fallback_excerpt="$2"

  printf '%s' "$fallback_excerpt"
}

format_outline_entry() {
  local entry="$1"
  if [[ "$entry" == \#* ]]; then
    printf '`%s`' "$entry"
  else
    printf '%s' "$entry"
  fi
}

split_joined_values() {
  local joined="$1"
  local old_ifs="$IFS"

  SPLIT_VALUES=()
  if [[ -z "$joined" ]]; then
    return 0
  fi

  IFS=$'\034' read -r -a SPLIT_VALUES <<< "$joined"
  IFS="$old_ifs"
}

is_part_file() {
  local name="$1"
  local middle

  if [[ -n "$suffix" ]]; then
    [[ "$name" == "$stem".*"$suffix" ]] || return 1
    middle="${name#"$stem".}"
    middle="${middle%"$suffix"}"
  else
    [[ "$name" == "$stem".* ]] || return 1
    middle="${name#"$stem".}"
  fi

  [[ "$middle" =~ ^[0-9]{3,}$ ]]
}

input_file=""
max_lines=500
dry_run=false
force=false

while (($# > 0)); do
  case "$1" in
    --max-lines)
      (($# >= 2)) || die '--max-lines requires a positive integer'
      max_lines="$2"
      shift
      ;;
    --dry-run)
      dry_run=true
      ;;
    --force)
      force=true
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    -*)
      die "unknown option: $1"
      ;;
    *)
      [[ -z "$input_file" ]] || die 'only one input file may be provided'
      input_file="$1"
      ;;
  esac
  shift
done

[[ -n "$input_file" ]] || {
  usage >&2
  exit 1
}

[[ "$max_lines" =~ ^[1-9][0-9]*$ ]] || die '--max-lines requires a positive integer'
[[ -f "$input_file" ]] || die "missing markdown file: $input_file"

case "$input_file" in
  */*)
    input_dir="${input_file%/*}"
    input_base="${input_file##*/}"
    ;;
  *)
    input_dir='.'
    input_base="$input_file"
    ;;
esac

suffix=""
stem="$input_base"
if [[ "$input_base" == *.* ]]; then
  suffix=".${input_base##*.}"
  stem="${input_base%"$suffix"}"
fi

backup_name="${stem}.original${suffix}"
backup_path="$input_dir/$backup_name"
source_mode="$(stat -f '%Lp' "$input_file")"

temp_dir="$(mktemp -d "$input_dir/.split-markdown-files.XXXXXX")"
status_file="$temp_dir/.status"
frontmatter_file="$temp_dir/.frontmatter"

cleanup() {
  rm -rf "$temp_dir"
}
trap cleanup EXIT

awk -v max_lines="$max_lines" \
    -v temp_dir="$temp_dir" \
    -v stem="$stem" \
    -v suffix="$suffix" \
    -v status_path="$status_file" \
    -v frontmatter_path="$frontmatter_file" '
  BEGIN {
    sep = sprintf("%c", 28)
    line_count = 0
    frontmatter_count = 0
    body_start = 1
    sec_count = 0
    root_count = 0
    part_count = 0
  }

  {
    sub(/\r$/, "", $0)
    line[++line_count] = $0
  }

  END {
    parse_frontmatter()
    write_frontmatter()

    if (line_count <= max_lines) {
      write_status("no_split")
      exit 0
    }

    parse_sections()
    split_root()
    write_status("split")

    for (part_index = 1; part_index <= part_count; part_index++) {
      write_part(part_index)
    }
  }

  function write_status(status) {
    print "status " status > status_path
    print "line_count " line_count >> status_path
    print "part_count " part_count >> status_path
    close(status_path)
  }

  function trim_left(value) {
    sub(/^[ \t]+/, "", value)
    return value
  }

  function trim_right(value) {
    sub(/[ \t]+$/, "", value)
    return value
  }

  function trim(value) {
    value = trim_left(value)
    value = trim_right(value)
    return value
  }

  function is_blank(value) {
    return value !~ /[^ \t]/
  }

  function markdown_trim_indent(value) {
    sub(/^[ \t]{0,3}/, "", value)
    return value
  }

  function match_fence_open(value,    stripped) {
    stripped = markdown_trim_indent(value)
    if (match(stripped, /^(`{3,}|~{3,})/)) {
      fence_open_char = substr(stripped, 1, 1)
      fence_open_len = RLENGTH
      return 1
    }
    return 0
  }

  function is_fence_close(value, expected_char, expected_len,    stripped, pattern) {
    stripped = markdown_trim_indent(value)
    if (expected_char == "`") {
      pattern = "^`{" expected_len ",}[ \t]*$"
    } else {
      pattern = "^~{" expected_len ",}[ \t]*$"
    }
    return stripped ~ pattern
  }

  function match_heading(value,    stripped, title) {
    stripped = markdown_trim_indent(value)
    if (!match(stripped, /^#{1,6}[ \t]+/)) {
      return 0
    }

    heading_level = RLENGTH - 1
    title = substr(stripped, RLENGTH + 1)
    title = trim_left(title)
    title = trim_right(title)
    sub(/[ \t]+#+[ \t]*$/, "", title)
    heading_title = trim_right(title)
    return 1
  }

  function clean_heading_title(value) {
    if (match_heading(value)) {
      return heading_title
    }
    return trim(value)
  }

  function append_space(list, value) {
    return list == "" ? value : list " " value
  }

  function append_context(context, heading_line) {
    return context == "" ? heading_line : context sep heading_line
  }

  function split_context(context, out,    count) {
    delete out
    if (context == "") {
      return 0
    }
    count = split(context, out, sep)
    return count
  }

  function context_label(context, continued,    ctx_lines, ctx_count, idx, label) {
    ctx_count = split_context(context, ctx_lines)
    if (ctx_count == 0) {
      label = "Document"
    } else {
      label = ""
      for (idx = 1; idx <= ctx_count; idx++) {
        label = label (idx == 1 ? "" : " > ") clean_heading_title(ctx_lines[idx])
      }
    }
    if (continued) {
      label = label " (continued)"
    }
    return label
  }

  function parse_frontmatter(    idx, found) {
    if (line_count > 0 && line[1] == "---") {
      found = 0
      for (idx = 2; idx <= line_count; idx++) {
        if (line[idx] == "---" || line[idx] == "...") {
          frontmatter_count = idx
          body_start = idx + 1
          found = 1
          break
        }
      }
      if (!found) {
        frontmatter_count = 0
        body_start = 1
      }
    }
  }

  function write_frontmatter(    idx) {
    if (frontmatter_count == 0) {
      close(frontmatter_path)
      return
    }
    for (idx = 1; idx <= frontmatter_count; idx++) {
      print line[idx] > frontmatter_path
    }
    close(frontmatter_path)
  }

  function parse_sections(    idx, section_id, parent_id) {
    in_fence = 0
    stack_count = 0

    for (idx = body_start; idx <= line_count; idx++) {
      current_line = line[idx]

      if (in_fence) {
        if (is_fence_close(current_line, fence_char, fence_len)) {
          in_fence = 0
        }
        continue
      }

      if (match_fence_open(current_line)) {
        in_fence = 1
        fence_char = fence_open_char
        fence_len = fence_open_len
        continue
      }

      if (!match_heading(current_line)) {
        continue
      }

      section_id = ++sec_count
      sec_start[section_id] = idx
      sec_end[section_id] = line_count + 1
      sec_level[section_id] = heading_level
      sec_line[section_id] = current_line
      sec_parent[section_id] = 0
      sec_children[section_id] = ""

      while (stack_count > 0 && sec_level[stack[stack_count]] >= heading_level) {
        sec_end[stack[stack_count]] = idx
        stack_count--
      }

      if (stack_count > 0) {
        parent_id = stack[stack_count]
        sec_parent[section_id] = parent_id
        sec_children[parent_id] = append_space(sec_children[parent_id], section_id)
      } else {
        root_child[++root_count] = section_id
      }

      stack[++stack_count] = section_id
    }
  }

  function range_has_nonblank(start, end,    idx) {
    for (idx = start; idx < end; idx++) {
      if (!is_blank(line[idx])) {
        return 1
      }
    }
    return 0
  }

  function count_rendered(context, start, end,    count, content_start, last_blank, ctx_lines, ctx_count, idx) {
    count = 0
    content_start = start
    last_blank = 0

    if (frontmatter_count > 0) {
      count += frontmatter_count
      last_blank = is_blank(line[frontmatter_count])
      if ((context != "" || start < end) && !last_blank) {
        count++
        last_blank = 1
      }
    }

    ctx_count = split_context(context, ctx_lines)
    for (idx = 1; idx <= ctx_count; idx++) {
      count++
      if (idx < ctx_count || start < end) {
        count++
        last_blank = 1
      } else {
        last_blank = 0
      }
    }

    if (last_blank) {
      while (content_start < end && is_blank(line[content_start])) {
        content_start++
      }
    }

    count += end - content_start
    return count
  }

  function fit_range(context, start, end) {
    return count_rendered(context, start, end) <= max_lines
  }

  function add_part(context, start, end, fallback) {
    part_context[++part_count] = context
    part_start[part_count] = start
    part_end[part_count] = end
    part_fallback[part_count] = fallback
  }

  function collect_blocks(start, end, block_starts, block_ends,    count, current_start, current_has_text, separator_pending, in_fence, local_fence_char, local_fence_len, idx, current_line) {
    delete block_starts
    delete block_ends
    count = 0
    current_start = 0
    current_has_text = 0
    separator_pending = 0
    in_fence = 0

    for (idx = start; idx < end; idx++) {
      current_line = line[idx]

      if (in_fence) {
        if (current_start == 0) {
          current_start = idx
        }
        current_has_text = 1
        if (is_fence_close(current_line, local_fence_char, local_fence_len)) {
          in_fence = 0
        }
        continue
      }

      if (match_fence_open(current_line)) {
        if (current_start == 0) {
          current_start = idx
        }
        current_has_text = 1
        in_fence = 1
        local_fence_char = fence_open_char
        local_fence_len = fence_open_len
        continue
      }

      if (is_blank(current_line)) {
        if (current_start == 0) {
          current_start = idx
        }
        if (current_has_text) {
          separator_pending = 1
        }
        continue
      }

      if (separator_pending && current_has_text) {
        block_starts[++count] = current_start
        block_ends[count] = idx
        current_start = idx
        current_has_text = 0
        separator_pending = 0
      }

      if (current_start == 0) {
        current_start = idx
      }
      current_has_text = 1
    }

    if (current_start > 0) {
      block_starts[++count] = current_start
      block_ends[count] = end
    }

    return count
  }

  function split_text_segment(start, end, context, fallback,    block_starts, block_ends, block_count, current_start, current_end, idx, block_start, block_end) {
    if (start >= end) {
      return
    }

    if (fit_range(context, start, end)) {
      add_part(context, start, end, fallback)
      return
    }

    block_count = collect_blocks(start, end, block_starts, block_ends)
    if (block_count <= 1) {
      add_part(context, start, end, fallback)
      return
    }

    current_start = 0
    current_end = 0

    for (idx = 1; idx <= block_count; idx++) {
      block_start = block_starts[idx]
      block_end = block_ends[idx]

      if (current_start == 0) {
        if (fit_range(context, block_start, block_end)) {
          current_start = block_start
          current_end = block_end
        } else {
          add_part(context, block_start, block_end, fallback)
        }
        continue
      }

      if (fit_range(context, current_start, block_end)) {
        current_end = block_end
        continue
      }

      add_part(context, current_start, current_end, fallback)
      current_start = 0
      current_end = 0

      if (fit_range(context, block_start, block_end)) {
        current_start = block_start
        current_end = block_end
      } else {
        add_part(context, block_start, block_end, fallback)
      }
    }

    if (current_start > 0) {
      add_part(context, current_start, current_end, fallback)
    }
  }

  function split_single_item(item_type, item_start, item_end, item_section, item_fallback, context) {
    if (item_type == "text") {
      split_text_segment(item_start, item_end, context, item_fallback)
    } else {
      split_section(item_section, context)
    }
  }

  function split_items(context, item_count, item_type, item_start, item_end, item_section, item_fallback,    current_start, current_end, current_fallback, idx) {
    current_start = 0
    current_end = 0
    current_fallback = ""

    for (idx = 1; idx <= item_count; idx++) {
      if (current_start == 0) {
        if (fit_range(context, item_start[idx], item_end[idx])) {
          current_start = item_start[idx]
          current_end = item_end[idx]
          current_fallback = item_fallback[idx]
        } else {
          split_single_item(item_type[idx], item_start[idx], item_end[idx], item_section[idx], item_fallback[idx], context)
        }
        continue
      }

      if (fit_range(context, current_start, item_end[idx])) {
        current_end = item_end[idx]
        if (current_fallback == "" && item_fallback[idx] != "") {
          current_fallback = item_fallback[idx]
        }
        continue
      }

      add_part(context, current_start, current_end, current_fallback)
      current_start = 0
      current_end = 0
      current_fallback = ""

      if (fit_range(context, item_start[idx], item_end[idx])) {
        current_start = item_start[idx]
        current_end = item_end[idx]
        current_fallback = item_fallback[idx]
      } else {
        split_single_item(item_type[idx], item_start[idx], item_end[idx], item_section[idx], item_fallback[idx], context)
      }
    }

    if (current_start > 0) {
      add_part(context, current_start, current_end, current_fallback)
    }
  }

  function split_section(section_id, context,    start, end, deeper_context, cursor, child_ids, child_count, child_index, child_id, item_count, item_type, item_start, item_end, item_section, item_fallback) {
    start = sec_start[section_id]
    end = sec_end[section_id]

    if (fit_range(context, start, end)) {
      add_part(context, start, end, "")
      return
    }

    deeper_context = append_context(context, sec_line[section_id])
    cursor = start + 1
    child_count = split(sec_children[section_id], child_ids, / +/)
    item_count = 0

    for (child_index = 1; child_index <= child_count; child_index++) {
      child_id = child_ids[child_index]
      if (child_id == "") {
        continue
      }

      if (cursor < sec_start[child_id] && range_has_nonblank(cursor, sec_start[child_id])) {
        item_type[++item_count] = "text"
        item_start[item_count] = cursor
        item_end[item_count] = sec_start[child_id]
        item_section[item_count] = 0
        item_fallback[item_count] = context_label(deeper_context, 1)
      }

      item_type[++item_count] = "section"
      item_start[item_count] = sec_start[child_id]
      item_end[item_count] = sec_end[child_id]
      item_section[item_count] = child_id
      item_fallback[item_count] = ""
      cursor = sec_end[child_id]
    }

    if (cursor < end && range_has_nonblank(cursor, end)) {
      item_type[++item_count] = "text"
      item_start[item_count] = cursor
      item_end[item_count] = end
      item_section[item_count] = 0
      item_fallback[item_count] = context_label(deeper_context, 1)
    }

    if (item_count == 0) {
      add_part(context, start, end, "")
      return
    }

    split_items(deeper_context, item_count, item_type, item_start, item_end, item_section, item_fallback)
  }

  function split_root(    item_count, item_type, item_start, item_end, item_section, item_fallback, idx) {
    item_count = 0

    if (root_count == 0) {
      if (body_start <= line_count) {
        split_text_segment(body_start, line_count + 1, "", "Document")
      } else {
        add_part("", body_start, body_start, "Document")
      }
      return
    }

    if (body_start < sec_start[root_child[1]] && range_has_nonblank(body_start, sec_start[root_child[1]])) {
      item_type[++item_count] = "text"
      item_start[item_count] = body_start
      item_end[item_count] = sec_start[root_child[1]]
      item_section[item_count] = 0
      item_fallback[item_count] = "Preamble"
    }

    for (idx = 1; idx <= root_count; idx++) {
      item_type[++item_count] = "section"
      item_start[item_count] = sec_start[root_child[idx]]
      item_end[item_count] = sec_end[root_child[idx]]
      item_section[item_count] = root_child[idx]
      item_fallback[item_count] = ""
    }

    if (item_count == 0) {
      add_part("", body_start, line_count + 1, "Document")
      return
    }

    split_items("", item_count, item_type, item_start, item_end, item_section, item_fallback)
  }

  function render_part(path, context, start, end,    ctx_lines, ctx_count, idx, content_start, wrote_blank) {
    if (frontmatter_count > 0) {
      for (idx = 1; idx <= frontmatter_count; idx++) {
        print line[idx] > path
      }
      if (context != "" || start < end) {
        if (!is_blank(line[frontmatter_count])) {
          print "" >> path
        }
      }
    }

    ctx_count = split_context(context, ctx_lines)
    for (idx = 1; idx <= ctx_count; idx++) {
      print ctx_lines[idx] >> path
      if (idx < ctx_count || start < end) {
        print "" >> path
      }
    }

    content_start = start
    if ((frontmatter_count > 0 || ctx_count > 0) && start < end) {
      while (content_start < end && is_blank(line[content_start])) {
        content_start++
      }
    }

    for (idx = content_start; idx < end; idx++) {
      print line[idx] >> path
    }
    close(path)
  }

  function extract_headings_range(start, end, headings,    idx, current_line, in_fence, local_fence_char, local_fence_len, count) {
    delete headings
    delete seen_heading
    count = 0
    in_fence = 0

    for (idx = start; idx < end; idx++) {
      current_line = line[idx]

      if (in_fence) {
        if (is_fence_close(current_line, local_fence_char, local_fence_len)) {
          in_fence = 0
        }
        continue
      }

      if (match_fence_open(current_line)) {
        in_fence = 1
        local_fence_char = fence_open_char
        local_fence_len = fence_open_len
        continue
      }

      if (match_heading(current_line) && !(current_line in seen_heading)) {
        headings[++count] = current_line
        seen_heading[current_line] = 1
      }
    }

    delete seen_heading
    return count
  }

  function sanitize_excerpt(value,    word_count, words, idx, rebuilt) {
    gsub(/\r/, " ", value)
    gsub(/\n/, " ", value)
    gsub(/\t/, " ", value)
    while (gsub(/  +/, " ", value)) {}
    value = trim(value)
    sub(/^Excerpt:[[:space:]]*/, "", value)
    sub(/^excerpt:[[:space:]]*/, "", value)
    sub(/^[-*][[:space:]]+/, "", value)
    sub(/^[0-9]+[.)][[:space:]]+/, "", value)
    gsub(/`/, "", value)
    value = trim(value)

    if (value ~ /^".*"$/ && length(value) >= 2) {
      value = substr(value, 2, length(value) - 2)
    } else if (value ~ /^'"'"'.*'"'"'$/ && length(value) >= 2) {
      value = substr(value, 2, length(value) - 2)
    }

    value = trim(value)
    if (value == "") {
      return value
    }

    word_count = split(value, words, /[[:space:]]+/)
    if (word_count > 45) {
      rebuilt = ""
      for (idx = 1; idx <= 45; idx++) {
        rebuilt = rebuilt (idx == 1 ? "" : " ") words[idx]
      }
      value = rebuilt "..."
    }

    return value
  }

  function fallback_excerpt(start, end, fallback,    idx, current_line, in_fence, local_fence_char, local_fence_len, paragraph, headings, heading_count, title_text, limit) {
    in_fence = 0
    paragraph = ""

    for (idx = start; idx < end; idx++) {
      current_line = line[idx]

      if (in_fence) {
        if (is_fence_close(current_line, local_fence_char, local_fence_len)) {
          in_fence = 0
        }
        continue
      }

      if (match_fence_open(current_line)) {
        in_fence = 1
        local_fence_char = fence_open_char
        local_fence_len = fence_open_len
        continue
      }

      if (match_heading(current_line)) {
        continue
      }

      if (is_blank(current_line)) {
        if (paragraph != "") {
          break
        }
        continue
      }

      current_line = trim(current_line)
      sub(/^[-*][[:space:]]+/, "", current_line)
      sub(/^[0-9]+[.)][[:space:]]+/, "", current_line)
      paragraph = paragraph (paragraph == "" ? "" : " ") current_line
    }

    if (paragraph != "") {
      return sanitize_excerpt(paragraph)
    }

    heading_count = extract_headings_range(start, end, headings)
    if (heading_count > 0) {
      title_text = ""
      limit = heading_count < 3 ? heading_count : 3
      for (idx = 1; idx <= limit; idx++) {
        title_text = title_text (idx == 1 ? "" : ", ") clean_heading_title(headings[idx])
      }
      return sanitize_excerpt("Covers " title_text ".")
    }

    if (fallback != "") {
      return sanitize_excerpt(fallback)
    }

    return "Continues the original document."
  }

  function write_part(part_index,    part_name, part_path, meta_path, rendered_lines, ctx_lines, ctx_count, idx, headings, heading_count, fallback_text, outline_written) {
    part_name = stem "." sprintf("%03d", part_index) suffix
    part_path = temp_dir "/" part_name
    meta_path = part_path ".meta"

    render_part(part_path, part_context[part_index], part_start[part_index], part_end[part_index])
    rendered_lines = count_rendered(part_context[part_index], part_start[part_index], part_end[part_index])
    fallback_text = fallback_excerpt(part_start[part_index], part_end[part_index], part_fallback[part_index])

    print "filename " part_name > meta_path
    print "line_count " rendered_lines >> meta_path
    print "over_limit " (rendered_lines > max_lines ? "true" : "false") >> meta_path

    ctx_count = split_context(part_context[part_index], ctx_lines)
    for (idx = 1; idx <= ctx_count; idx++) {
      print "context " ctx_lines[idx] >> meta_path
    }

    heading_count = extract_headings_range(part_start[part_index], part_end[part_index], headings)
    outline_written = 0
    for (idx = 1; idx <= heading_count; idx++) {
      print "outline " headings[idx] >> meta_path
      outline_written = 1
    }

    if (!outline_written) {
      if (part_fallback[part_index] != "") {
        print "outline " part_fallback[part_index] >> meta_path
      } else if (part_context[part_index] != "") {
        print "outline " context_label(part_context[part_index], 1) >> meta_path
      }
    }

    print "excerpt_fallback " fallback_text >> meta_path
    close(meta_path)
  }
' "$input_file"

status=''
input_line_count=''
planned_part_count=''

while IFS= read -r status_line || [[ -n "$status_line" ]]; do
  key="${status_line%% *}"
  if [[ "$status_line" == *" "* ]]; then
    value="${status_line#* }"
  else
    value=''
  fi

  case "$key" in
    status)
      status="$value"
      ;;
    line_count)
      input_line_count="$value"
      ;;
    part_count)
      planned_part_count="$value"
      ;;
  esac
done < "$status_file"

if [[ "$status" == 'no_split' ]]; then
  printf 'No split needed: %s has %s lines (limit %s).\n' "$input_file" "$input_line_count" "$max_lines"
  exit 0
fi

[[ "$status" == 'split' ]] || die 'failed to build split plan'

shopt -s nullglob
meta_files=( "$temp_dir"/*.meta )
shopt -u nullglob

[[ ${#meta_files[@]} -gt 0 ]] || die 'split planner produced no part metadata'
[[ ${#meta_files[@]} -eq ${planned_part_count:-0} ]] || die 'split planner metadata count mismatch'

part_names=()
part_paths=()
part_line_counts=()
part_over_limit=()
part_contexts=()
part_outlines=()
part_fallback_excerpts=()
part_excerpts=()

for meta_file in "${meta_files[@]}"; do
  filename=''
  line_count_value=''
  over_limit_value='false'
  context_joined=''
  outline_joined=''
  fallback_excerpt_value=''

  while IFS= read -r meta_line || [[ -n "$meta_line" ]]; do
    key="${meta_line%% *}"
    if [[ "$meta_line" == *" "* ]]; then
      value="${meta_line#* }"
    else
      value=''
    fi

    case "$key" in
      filename)
        filename="$value"
        ;;
      line_count)
        line_count_value="$value"
        ;;
      over_limit)
        over_limit_value="$value"
        ;;
      context)
        if [[ -n "$context_joined" ]]; then
          context_joined+=$'\034'
        fi
        context_joined+="$value"
        ;;
      outline)
        if [[ -n "$outline_joined" ]]; then
          outline_joined+=$'\034'
        fi
        outline_joined+="$value"
        ;;
      excerpt_fallback)
        fallback_excerpt_value="$value"
        ;;
    esac
  done < "$meta_file"

  [[ -n "$filename" ]] || die "missing filename in metadata: $meta_file"

  part_names+=( "$filename" )
  part_paths+=( "$input_dir/$filename" )
  part_line_counts+=( "$line_count_value" )
  part_over_limit+=( "$over_limit_value" )
  part_contexts+=( "$context_joined" )
  part_outlines+=( "$outline_joined" )
  part_fallback_excerpts+=( "$fallback_excerpt_value" )
done

if [[ "$dry_run" == 'true' ]]; then
  printf 'Would split %s (%s lines) into %s parts.\n' "$input_file" "$input_line_count" "$planned_part_count"
  printf 'Backup: %s\n' "$backup_name"
  printf 'Index: %s\n' "$input_base"
  for ((i = 0; i < ${#part_names[@]}; i += 1)); do
    printf 'Part %03d: %s (%s lines)\n' "$((i + 1))" "${part_names[i]}" "${part_line_counts[i]}"
    split_joined_values "${part_contexts[i]}"
    context_items=( "${SPLIT_VALUES[@]}" )
    if ((${#context_items[@]} > 0)); then
      printf '  Context: '
      for ((j = 0; j < ${#context_items[@]}; j += 1)); do
        ((j == 0)) || printf ', '
        format_outline_entry "${context_items[j]}"
      done
      printf '\n'
    fi
    split_joined_values "${part_outlines[i]}"
    outline_items=( "${SPLIT_VALUES[@]}" )
    for outline_item in "${outline_items[@]}"; do
      printf '  %s\n' "$outline_item"
    done
  done
  exit 0
fi

existing_part_paths=()
shopt -s nullglob
for candidate in "$input_dir"/*; do
  [[ -f "$candidate" ]] || continue
  if is_part_file "$(basename "$candidate")"; then
    existing_part_paths+=( "$candidate" )
  fi
done
shopt -u nullglob

collisions=()
if [[ -e "$backup_path" && "$force" != 'true' ]]; then
  collisions+=( "$backup_path" )
fi

if [[ "$force" != 'true' ]]; then
  for part_path in "${part_paths[@]}"; do
    if [[ -e "$part_path" ]]; then
      collisions+=( "$part_path" )
    fi
  done
fi

if ((${#collisions[@]} > 0)); then
  printf '%s\n' 'refusing to overwrite existing files without --force:' >&2
  printf '%s\n' "${collisions[@]}" >&2
  exit 1
fi

for ((i = 0; i < ${#part_names[@]}; i += 1)); do
  part_excerpts+=( "$(summarize_part "$temp_dir/${part_names[i]}" "${part_fallback_excerpts[i]}")" )
done

temp_index_path="$temp_dir/$input_base"
temp_backup_path="$temp_dir/$backup_name"

cp -p "$input_file" "$temp_backup_path"

{
  if [[ -s "$frontmatter_file" ]]; then
    cat "$frontmatter_file"
    printf '\n'
  fi

  printf '# Split Index\n\n'
  printf 'This document was split because it exceeded %s lines.\n\n' "$max_lines"
  printf 'Original backup: [%s](<%s>)\n\n' "$backup_name" "$backup_name"

  for ((i = 0; i < ${#part_names[@]}; i += 1)); do
    printf '## [%s](<%s>)\n\n' "${part_names[i]}" "${part_names[i]}"
    printf 'Lines: %s\n' "${part_line_counts[i]}"

    split_joined_values "${part_contexts[i]}"
    context_items=( "${SPLIT_VALUES[@]}" )
    if ((${#context_items[@]} > 0)); then
      printf 'Context: '
      for ((j = 0; j < ${#context_items[@]}; j += 1)); do
        ((j == 0)) || printf ', '
        format_outline_entry "${context_items[j]}"
      done
      printf '\n'
    fi

    if [[ "${part_over_limit[i]}" == 'true' ]]; then
      printf 'Note: This part is over the target limit because a single block could not be split safely.\n'
    fi

    printf '\nOutline:\n'
    split_joined_values "${part_outlines[i]}"
    outline_items=( "${SPLIT_VALUES[@]}" )
    if ((${#outline_items[@]} > 0)); then
      for outline_item in "${outline_items[@]}"; do
        printf -- '- '
        format_outline_entry "$outline_item"
        printf '\n'
      done
    else
      printf -- '- Document continuation\n'
    fi

    printf '\nExcerpt: %s\n\n' "${part_excerpts[i]}"
  done
} > "$temp_index_path"

chmod "$source_mode" "$temp_index_path"
for part_name in "${part_names[@]}"; do
  chmod "$source_mode" "$temp_dir/$part_name"
done

mv "$temp_backup_path" "$backup_path"
for ((i = 0; i < ${#part_names[@]}; i += 1)); do
  mv "$temp_dir/${part_names[i]}" "${part_paths[i]}"
done
mv "$temp_index_path" "$input_file"

if [[ "$force" == 'true' ]]; then
  for stale_part in "${existing_part_paths[@]}"; do
    keep_part=false
    for part_path in "${part_paths[@]}"; do
      if [[ "$stale_part" == "$part_path" ]]; then
        keep_part=true
        break
      fi
    done
    if [[ "$keep_part" == 'false' ]]; then
      rm -f "$stale_part"
    fi
  done
fi

printf 'Split %s into %s parts.\n' "$input_file" "$planned_part_count"
printf 'Backup: %s\n' "$backup_path"
printf 'Index: %s\n' "$input_file"
for ((i = 0; i < ${#part_paths[@]}; i += 1)); do
  printf 'Part: %s [%s lines]' "${part_paths[i]}" "${part_line_counts[i]}"
  if [[ "${part_over_limit[i]}" == 'true' ]]; then
    printf ' (over limit, best-effort)'
  fi
  printf '\n'
done
