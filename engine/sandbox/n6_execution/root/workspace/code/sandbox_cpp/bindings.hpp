/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

#pragma once

#include <cstdlib>
#include <filesystem>
#include <string>

#include "wrapper/runtime.hpp"

namespace demo {

class Service {
public:
    // <brique:capacity name="sandbox.cpp.echo">
    std::string echo(const wrapper::ParamsView& params) const {
        const std::string message = params.string_value("message", "");
        return "{"
               "\"echo\":" + wrapper::json::quote(message) + ","
               "\"handled_by\":\"sandbox_cpp.echo\""
               "}";
    }
    // </brique:capacity>

    // <brique:capacity name="sandbox.cpp.runtime.info">
    std::string runtime_info() const {
        std::error_code ec;
        const auto cwd = std::filesystem::current_path(ec);
        const std::string workdir = ec ? std::string() : cwd.string();
        return "{"
               "\"wrapper_name\":" + wrapper::json::quote(wrapper::env_or("BRIQUE_WRAPPER_NAME")) + ","
               "\"ctx_id\":" + wrapper::json::quote(wrapper::env_or("BRIQUE_CTX_ID")) + ","
               "\"ctx_dir\":" + wrapper::json::quote(wrapper::env_or("BRIQUE_CTX_DIR")) + ","
               "\"wrapper_src_dir\":" + wrapper::json::quote(wrapper::env_or("BRIQUE_WRAPPER_SRC_DIR")) + ","
               "\"wrapper_build_dir\":" + wrapper::json::quote(wrapper::env_or("BRIQUE_WRAPPER_BUILD_DIR")) + ","
               "\"workdir_env\":" + wrapper::json::quote(wrapper::env_or("BRIQUE_WORKDIR")) + ","
               "\"cwd\":" + wrapper::json::quote(workdir) +
               "}";
    }
    // </brique:capacity>
};

}  // namespace demo

inline wrapper::BindingsFragment build_bindings(wrapper::Runtime&) {
    auto service = std::make_shared<demo::Service>();
    wrapper::BindingsFragment fragment;

    fragment.capacities.push_back(wrapper::CapacityBinding{
        "",
        "sandbox.cpp.echo",
        [service](const wrapper::ParamsView& params) { return service->echo(params); },
    });

    fragment.capacities.push_back(wrapper::CapacityBinding{
        "",
        "sandbox.cpp.runtime.info",
        [service](const wrapper::ParamsView&) { return service->runtime_info(); },
    });

    return fragment;
}
