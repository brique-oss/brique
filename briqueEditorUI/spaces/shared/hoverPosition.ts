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


/** Gap between the pointer and the popover. */
export const POPOVER_OFFSET = 12;

export type PopoverPlace = { left?: number; right?: number; top?: number; bottom?: number };

/**
 * Where a popover opened by the pointer goes: away from the nearest edge of the viewport, so that it has room to open. In the
 * right half of the window it is anchored by its right edge and extends to the left of the pointer, and likewise for the bottom.
 */
export function popoverPlace(pointer: { x: number; y: number }, viewport: { width: number; height: number }): PopoverPlace {
  const onRight = pointer.x > viewport.width / 2;
  const onBottom = pointer.y > viewport.height / 2;
  return {
    ...(onRight ? { right: viewport.width - pointer.x + POPOVER_OFFSET } : { left: pointer.x + POPOVER_OFFSET }),
    ...(onBottom ? { bottom: viewport.height - pointer.y + POPOVER_OFFSET } : { top: pointer.y + POPOVER_OFFSET }),
  };
}
