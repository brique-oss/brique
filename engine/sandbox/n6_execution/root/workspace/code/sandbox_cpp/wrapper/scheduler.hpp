/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

#pragma once

#include <condition_variable>
#include <cstddef>
#include <functional>
#include <future>
#include <memory>
#include <mutex>
#include <string>
#include <stdexcept>
#include <thread>
#include <unordered_map>

namespace wrapper {

class WorkScheduler;

// The execution slot the work running on this thread holds, if it runs under a scheduler.
struct WorkLease {
    WorkScheduler* scheduler = nullptr;
    bool holding = false;
};

inline thread_local WorkLease* current_work_lease = nullptr;

class WorkScheduler {
public:
    WorkScheduler(std::size_t max_concurrency, std::size_t queue_limit)
        : max_concurrency_(max_concurrency), queue_limit_(queue_limit) {
        if (max_concurrency_ == 0 || queue_limit_ == 0) {
            throw std::runtime_error("scheduler limits must be positive");
        }
    }

    ~WorkScheduler() { close(); }

    // With ordered=true the work runs after the work already queued under the same key; with
    // ordered=false it only waits for a free slot, whatever else is queued under its key.
    bool submit(const std::string& key, std::function<void()> work, bool ordered = true) {
        std::shared_future<void> previous;
        auto completed = std::make_shared<std::promise<void>>();
        const auto done = completed->get_future().share();
        auto token = std::make_shared<int>(0);
        {
            std::lock_guard<std::mutex> lock(mu_);
            if (!accepting_ || pending_ >= max_concurrency_ + queue_limit_) {
                return false;
            }
            if (ordered) {
                const auto it = tails_.find(key);
                if (it != tails_.end()) {
                    previous = it->second.future;
                }
                tails_[key] = Tail{done, token};
            }
            ++pending_;
        }
        std::thread([this, key, previous, token, ordered, completed, work = std::move(work)]() mutable {
            if (previous.valid()) {
                previous.wait();
            }
            {
                std::unique_lock<std::mutex> lock(mu_);
                cv_.wait(lock, [this] { return active_ < max_concurrency_; });
                ++active_;
            }
            WorkLease lease{this, true};
            current_work_lease = &lease;
            try {
                work();
            } catch (...) {
                // Runtime work records its own error; the scheduler keeps progressing.
            }
            current_work_lease = nullptr;
            {
                std::lock_guard<std::mutex> lock(mu_);
                if (lease.holding) {
                    --active_;
                }
                --pending_;
                if (ordered) {
                    const auto it = tails_.find(key);
                    if (it != tails_.end() && it->second.token == token) {
                        tails_.erase(it);
                    }
                }
            }
            completed->set_value();
            cv_.notify_all();
        }).detach();
        return true;
    }

    // Give an execution slot back, and take one again (in turn). Used through SlotYield.
    void release_slot() {
        {
            std::lock_guard<std::mutex> lock(mu_);
            --active_;
        }
        cv_.notify_all();
    }

    void acquire_slot() {
        std::unique_lock<std::mutex> lock(mu_);
        cv_.wait(lock, [this] { return active_ < max_concurrency_; });
        ++active_;
    }

    std::size_t queued() const {
        std::lock_guard<std::mutex> lock(mu_);
        return pending_ > active_ ? pending_ - active_ : 0;
    }

    void close() {
        std::unique_lock<std::mutex> lock(mu_);
        accepting_ = false;
        cv_.wait(lock, [this] { return pending_ == 0; });
    }

private:
    struct Tail {
        std::shared_future<void> future;
        std::shared_ptr<int> token;
    };
    std::size_t max_concurrency_;
    std::size_t queue_limit_;
    mutable std::mutex mu_;
    std::condition_variable cv_;
    std::size_t pending_ = 0;
    std::size_t active_ = 0;
    bool accepting_ = true;
    std::unordered_map<std::string, Tail> tails_;
};

// Gives the execution slot of the work running on this thread back while the thread waits for the
// answer to a call it made, and takes it back, in turn, when it goes out of scope. Without it, as
// many calls as there are slots, each waiting for a call that needs a slot itself, would wait for
// each other for ever. Outside scheduled work this does nothing.
class SlotYield {
public:
    SlotYield() : lease_(current_work_lease) {
        if (lease_ != nullptr && lease_->holding) {
            lease_->holding = false;
            lease_->scheduler->release_slot();
            yielded_ = true;
        }
    }
    ~SlotYield() {
        if (yielded_) {
            lease_->scheduler->acquire_slot();
            lease_->holding = true;
        }
    }
    SlotYield(const SlotYield&) = delete;
    SlotYield& operator=(const SlotYield&) = delete;

private:
    WorkLease* lease_;
    bool yielded_ = false;
};

}  // namespace wrapper
