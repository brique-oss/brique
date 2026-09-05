#pragma once

#include <cctype>
#include <sstream>
#include <stdexcept>
#include <string>

namespace wrapper::json {

inline std::string quote(const std::string& input) {
    std::ostringstream out;
    out << '"';
    for (unsigned char ch : input) {
        switch (ch) {
            case '\\':
                out << "\\\\";
                break;
            case '"':
                out << "\\\"";
                break;
            case '\b':
                out << "\\b";
                break;
            case '\f':
                out << "\\f";
                break;
            case '\n':
                out << "\\n";
                break;
            case '\r':
                out << "\\r";
                break;
            case '\t':
                out << "\\t";
                break;
            default:
                if (ch < 0x20) {
                    static const char* hex = "0123456789abcdef";
                    out << "\\u00" << hex[(ch >> 4) & 0xF] << hex[ch & 0xF];
                } else {
                    out << static_cast<char>(ch);
                }
                break;
        }
    }
    out << '"';
    return out.str();
}

inline std::size_t skip_ws(const std::string& raw, std::size_t pos) {
    while (pos < raw.size() && std::isspace(static_cast<unsigned char>(raw[pos])) != 0) {
        ++pos;
    }
    return pos;
}

inline std::size_t find_key(const std::string& raw, const std::string& key) {
    const std::string needle = quote(key);
    std::size_t pos = 0;
    while ((pos = raw.find(needle, pos)) != std::string::npos) {
        std::size_t colon = skip_ws(raw, pos + needle.size());
        if (colon < raw.size() && raw[colon] == ':') {
            return colon + 1;
        }
        pos += needle.size();
    }
    return std::string::npos;
}

inline std::string parse_string_literal(const std::string& raw, std::size_t quote_pos) {
    if (quote_pos >= raw.size() || raw[quote_pos] != '"') {
        throw std::runtime_error("expected json string literal");
    }
    std::string out;
    bool escape = false;
    for (std::size_t i = quote_pos + 1; i < raw.size(); ++i) {
        char ch = raw[i];
        if (escape) {
            switch (ch) {
                case '"':
                case '\\':
                case '/':
                    out.push_back(ch);
                    break;
                case 'b':
                    out.push_back('\b');
                    break;
                case 'f':
                    out.push_back('\f');
                    break;
                case 'n':
                    out.push_back('\n');
                    break;
                case 'r':
                    out.push_back('\r');
                    break;
                case 't':
                    out.push_back('\t');
                    break;
                default:
                    out.push_back(ch);
                    break;
            }
            escape = false;
            continue;
        }
        if (ch == '\\') {
            escape = true;
            continue;
        }
        if (ch == '"') {
            return out;
        }
        out.push_back(ch);
    }
    throw std::runtime_error("unterminated json string literal");
}

inline std::size_t match_balanced(const std::string& raw, std::size_t start, char open_ch, char close_ch) {
    if (start >= raw.size() || raw[start] != open_ch) {
        throw std::runtime_error("json balanced match start mismatch");
    }
    bool in_string = false;
    bool escape = false;
    int depth = 0;
    for (std::size_t i = start; i < raw.size(); ++i) {
        char ch = raw[i];
        if (in_string) {
            if (escape) {
                escape = false;
                continue;
            }
            if (ch == '\\') {
                escape = true;
                continue;
            }
            if (ch == '"') {
                in_string = false;
            }
            continue;
        }
        if (ch == '"') {
            in_string = true;
            continue;
        }
        if (ch == open_ch) {
            ++depth;
            continue;
        }
        if (ch == close_ch) {
            --depth;
            if (depth == 0) {
                return i;
            }
        }
    }
    throw std::runtime_error("unterminated balanced json fragment");
}

inline std::string object_field(const std::string& raw, const std::string& key, const std::string& fallback = "{}") {
    std::size_t pos = find_key(raw, key);
    if (pos == std::string::npos) {
        return fallback;
    }
    pos = skip_ws(raw, pos);
    if (pos >= raw.size() || raw[pos] != '{') {
        return fallback;
    }
    const std::size_t end = match_balanced(raw, pos, '{', '}');
    return raw.substr(pos, end - pos + 1);
}

inline std::string array_field(const std::string& raw, const std::string& key, const std::string& fallback = "[]") {
    std::size_t pos = find_key(raw, key);
    if (pos == std::string::npos) {
        return fallback;
    }
    pos = skip_ws(raw, pos);
    if (pos >= raw.size() || raw[pos] != '[') {
        return fallback;
    }
    const std::size_t end = match_balanced(raw, pos, '[', ']');
    return raw.substr(pos, end - pos + 1);
}

inline std::string string_field(const std::string& raw, const std::string& key, const std::string& fallback = "") {
    std::size_t pos = find_key(raw, key);
    if (pos == std::string::npos) {
        return fallback;
    }
    pos = skip_ws(raw, pos);
    if (pos >= raw.size() || raw[pos] != '"') {
        return fallback;
    }
    try {
        return parse_string_literal(raw, pos);
    } catch (...) {
        return fallback;
    }
}

inline bool has_key(const std::string& raw, const std::string& key) {
    return find_key(raw, key) != std::string::npos;
}

}  // namespace wrapper::json
