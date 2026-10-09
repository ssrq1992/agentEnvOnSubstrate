/// A wrapper that unconditionally implements [`Send`], used to work around a
/// rustc limitation where the compiler cannot prove that certain async
/// combinators (e.g. `buffer_unordered`) produce `Send` futures even when all
/// inputs are `Send`.
///
/// **Construction is restricted to `T: Send`.** The private `marker` field
/// (`PhantomData<fn() -> *mut T>`) prevents any code outside this module from
/// constructing `AlwaysSend<T>` without going through [`AlwaysSend::new`], which
/// requires the `T: Send` bound. Therefore no `AlwaysSend<T>` where `T: !Send`
/// can ever exist at runtime, making the `unsafe impl Send` below sound.
///
/// See the upstream discussion for context on when this pattern is necessary:
/// <https://users.rust-lang.org/t/implementation-of-trait-is-not-general-enough-when-used-inside-tokio-spawn/122490/4>
pub struct AlwaysSend<T> {
    pub inner: T,
    /// Private field that prevents construction of `AlwaysSend<T>` other than
    /// through [`AlwaysSend::new`], which requires `T: Send`.
    marker: std::marker::PhantomData<fn() -> *mut T>,
}

// SAFETY: Every `AlwaysSend<T>` is constructed via `AlwaysSend::new`, which
// requires `T: Send`. The private `marker` field makes it impossible to
// construct `AlwaysSend<T>` in any other way (the struct literal syntax is
// unavailable outside this module). Therefore, at runtime, the inner `T` is
// always `Send`, and this impl is sound.
unsafe impl<T> Send for AlwaysSend<T> {}

/// This wrapper offers structural pinning of the [`inner`][AlwaysSend::inner] field.
impl<T: Unpin> Unpin for AlwaysSend<T> {}

impl<T: Send> AlwaysSend<T> {
    pub fn new(inner: T) -> Self {
        AlwaysSend {
            inner,
            marker: std::marker::PhantomData,
        }
    }
}

impl<T> AlwaysSend<T> {
    /// Pinned mutable access to self.[inner][Self::inner].
    pub fn inner_pin_mut(self: std::pin::Pin<&mut Self>) -> std::pin::Pin<&mut T> {
        // SAFETY: field is structurally pinned
        unsafe { self.map_unchecked_mut(|this| &mut this.inner) }
    }
}

impl<F: std::future::Future> std::future::Future for AlwaysSend<F> {
    type Output = F::Output;

    fn poll(
        self: std::pin::Pin<&mut Self>,
        cx: &mut core::task::Context<'_>,
    ) -> core::task::Poll<Self::Output> {
        self.inner_pin_mut().poll(cx)
    }
}
