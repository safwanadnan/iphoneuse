#import <XCTest/XCTest.h>

NS_ASSUME_NONNULL_BEGIN

/// Thin wrappers around private XCTest API. Every call is guarded with
/// respondsToSelector: and returns nil/NO when Apple changes things, so the
/// helper degrades instead of crashing.
@interface IPUPrivate : NSObject

/// The app currently in the foreground (not SpringBoard when another app is up).
+ (nullable XCUIApplication *)activeApplication NS_SWIFT_NAME(activeApplication());

/// Bundle identifier of an XCUIApplication, if XCTest exposes it.
+ (nullable NSString *)bundleIDOfApplication:(XCUIApplication *)app NS_SWIFT_NAME(bundleID(of:));

/// Turn off "wait for app to be idle" so taps don't stall on animated apps.
+ (void)disableQuiescenceWaitFor:(XCUIApplication *)app NS_SWIFT_NAME(disableQuiescenceWait(for:));

/// Press the side (lock) button.
+ (BOOL)pressLockButton NS_SWIFT_NAME(pressLockButton());

@end

NS_ASSUME_NONNULL_END
