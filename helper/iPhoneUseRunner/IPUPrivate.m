#import "IPUPrivate.h"
#import <objc/message.h>

#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Warc-performSelector-leaks"
#pragma clang diagnostic ignored "-Wundeclared-selector"

static NSString *const kSpringBoard = @"com.apple.springboard";

@implementation IPUPrivate

+ (nullable id)axClient
{
  Class cls = NSClassFromString(@"XCAXClient_iOS");
  if (cls == nil || ![cls respondsToSelector:@selector(sharedClient)]) {
    return nil;
  }
  return [cls performSelector:@selector(sharedClient)];
}

+ (nullable XCUIApplication *)applicationWithPID:(pid_t)pid
{
  Class app = XCUIApplication.class;
  SEL selectors[] = { NSSelectorFromString(@"appWithPID:"), NSSelectorFromString(@"applicationWithPID:") };
  for (int i = 0; i < 2; i++) {
    if ([app respondsToSelector:selectors[i]]) {
      return ((id (*)(id, SEL, pid_t))objc_msgSend)(app, selectors[i], pid);
    }
  }
  return nil;
}

+ (nullable XCUIApplication *)activeApplication
{
  id client = [self axClient];
  SEL activeSel = NSSelectorFromString(@"activeApplications");
  if (client == nil || ![client respondsToSelector:activeSel]) {
    return nil;
  }
  NSArray *elements = [client performSelector:activeSel];
  XCUIApplication *springboard = nil;
  for (id element in elements) {
    NSNumber *pidNumber = nil;
    @try {
      pidNumber = [element valueForKey:@"processIdentifier"];
    } @catch (NSException *e) {
      continue;
    }
    if (pidNumber == nil) {
      continue;
    }
    XCUIApplication *app = [self applicationWithPID:pidNumber.intValue];
    if (app == nil) {
      continue;
    }
    NSString *bundleID = [self bundleIDOfApplication:app];
    if ([bundleID isEqualToString:kSpringBoard]) {
      springboard = app;
      continue;
    }
    return app;
  }
  return springboard;
}

+ (nullable NSString *)bundleIDOfApplication:(XCUIApplication *)app
{
  for (NSString *key in @[ @"bundleID", @"bundleIdentifier" ]) {
    @try {
      id value = [app valueForKey:key];
      if ([value isKindOfClass:NSString.class] && [value length] > 0) {
        return value;
      }
    } @catch (NSException *e) {
    }
  }
  return nil;
}

+ (void)disableQuiescenceWaitFor:(XCUIApplication *)app
{
  @try {
    SEL processSel = NSSelectorFromString(@"applicationImpl");
    id impl = [app respondsToSelector:processSel] ? [app performSelector:processSel] : nil;
    id process = nil;
    SEL currentProcess = NSSelectorFromString(@"currentProcess");
    if (impl != nil && [impl respondsToSelector:currentProcess]) {
      process = [impl performSelector:currentProcess];
    }
    SEL setter = NSSelectorFromString(@"setShouldWaitForQuiescence:");
    if (process != nil && [process respondsToSelector:setter]) {
      ((void (*)(id, SEL, BOOL))objc_msgSend)(process, setter, NO);
    }
  } @catch (NSException *e) {
  }
}

+ (BOOL)pressLockButton
{
  XCUIDevice *device = XCUIDevice.sharedDevice;
  SEL sel = NSSelectorFromString(@"pressLockButton");
  if (![device respondsToSelector:sel]) {
    return NO;
  }
  [device performSelector:sel];
  return YES;
}

@end

#pragma clang diagnostic pop
